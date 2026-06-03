from __future__ import annotations

import secrets
import sqlite3
from pathlib import Path

from fastapi import Depends, FastAPI, File, Form, HTTPException, Request, Response, UploadFile
from fastapi.responses import FileResponse, HTMLResponse, RedirectResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates

from receipt_upload.config import Settings, load_settings
from receipt_upload.db import connect, init_db, row, rows
from receipt_upload.imagemagick import ImageMagickUnavailable, ensure_imagemagick
from receipt_upload.security import (
    SESSION_COOKIE,
    clear_login_attempts,
    client_ip,
    is_ip_banned,
    purge_old_login_attempts,
    read_session,
    record_failed_login,
    sign_session,
)
from receipt_upload.storage import directory_size, human_bytes, save_receipt_pdf

settings = load_settings()
settings.data_dir.mkdir(parents=True, exist_ok=True)
settings.upload_dir.mkdir(parents=True, exist_ok=True)
init_db(settings.data_dir / "app.sqlite3")

app = FastAPI(title="receipt-upload")
templates = Jinja2Templates(directory=str(Path(__file__).parent / "templates"))
app.mount("/static", StaticFiles(directory=str(Path(__file__).parent / "static")), name="static")


def run() -> None:
    from receipt_upload.cli import main

    main()


def get_conn() -> sqlite3.Connection:
    with connect(settings.data_dir / "app.sqlite3") as conn:
        yield conn


def render(request: Request, template: str, context: dict | None = None, status_code: int = 200) -> HTMLResponse:
    return templates.TemplateResponse(
        request,
        template,
        {"settings": settings, **(context or {})},
        status_code=status_code,
    )


def require_admin(request: Request) -> None:
    session = read_session(request.cookies.get(SESSION_COOKIE), settings.secret_key)
    if not session or session.get("admin") is not True:
        raise HTTPException(status_code=303, headers={"Location": "/admin/login"})


@app.on_event("startup")
def startup_check() -> None:
    ensure_imagemagick(settings.auto_install_imagemagick)


@app.get("/", include_in_schema=False)
def root() -> RedirectResponse:
    return RedirectResponse("/admin/login", status_code=303)


@app.get("/admin/login", response_class=HTMLResponse)
def login_page(request: Request) -> HTMLResponse:
    return render(request, "login.html")


@app.post("/admin/login")
def login(
    request: Request,
    username: str = Form(...),
    password: str = Form(...),
    conn: sqlite3.Connection = Depends(get_conn),
) -> Response:
    ip_address = client_ip(request)
    purge_old_login_attempts(conn)
    if is_ip_banned(conn, ip_address):
        return render(request, "login.html", {"error": "Too many failed attempts. Try again later."}, 429)
    valid = secrets.compare_digest(username, settings.admin_username) and secrets.compare_digest(
        password, settings.admin_password
    )
    if not valid:
        record_failed_login(conn, ip_address)
        return render(request, "login.html", {"error": "Invalid username or password."}, 401)
    clear_login_attempts(conn, ip_address)
    response = RedirectResponse("/admin", status_code=303)
    response.set_cookie(
        SESSION_COOKIE,
        sign_session({"admin": True}, settings.secret_key),
        httponly=True,
        samesite="lax",
        secure=settings.app_base_url.startswith("https://"),
    )
    return response


@app.post("/admin/logout")
def logout() -> RedirectResponse:
    response = RedirectResponse("/admin/login", status_code=303)
    response.delete_cookie(SESSION_COOKIE)
    return response


@app.get("/admin", response_class=HTMLResponse)
def admin_dashboard(
    request: Request,
    _: None = Depends(require_admin),
    conn: sqlite3.Connection = Depends(get_conn),
) -> HTMLResponse:
    upload_rows = rows(
        conn,
        "SELECT * FROM uploads ORDER BY archived_at IS NOT NULL, created_at DESC",
    )
    return render(
        request,
        "admin.html",
        {
            "cardholders": rows(conn, "SELECT * FROM cardholders ORDER BY name COLLATE NOCASE"),
            "stores": rows(conn, "SELECT * FROM stores ORDER BY name COLLATE NOCASE"),
            "uploads": upload_rows,
            "disk_usage": human_bytes(directory_size(settings.data_dir)),
        },
    )


@app.post("/admin/cardholders")
def add_cardholder(
    name: str = Form(...),
    _: None = Depends(require_admin),
    conn: sqlite3.Connection = Depends(get_conn),
) -> RedirectResponse:
    conn.execute("INSERT OR IGNORE INTO cardholders (name) VALUES (?)", (name.strip(),))
    return RedirectResponse("/admin", status_code=303)


@app.post("/admin/cardholders/{cardholder_id}/delete")
def delete_cardholder(
    cardholder_id: int,
    _: None = Depends(require_admin),
    conn: sqlite3.Connection = Depends(get_conn),
) -> RedirectResponse:
    conn.execute("DELETE FROM cardholders WHERE id = ?", (cardholder_id,))
    return RedirectResponse("/admin", status_code=303)


@app.post("/admin/stores")
def add_store(
    name: str = Form(...),
    _: None = Depends(require_admin),
    conn: sqlite3.Connection = Depends(get_conn),
) -> RedirectResponse:
    conn.execute("INSERT OR IGNORE INTO stores (name) VALUES (?)", (name.strip(),))
    return RedirectResponse("/admin", status_code=303)


@app.post("/admin/stores/{store_id}/delete")
def delete_store(
    store_id: int,
    _: None = Depends(require_admin),
    conn: sqlite3.Connection = Depends(get_conn),
) -> RedirectResponse:
    conn.execute("DELETE FROM stores WHERE id = ?", (store_id,))
    return RedirectResponse("/admin", status_code=303)


@app.post("/admin/uploads/{upload_id}/archive")
def archive_upload(
    upload_id: int,
    _: None = Depends(require_admin),
    conn: sqlite3.Connection = Depends(get_conn),
) -> RedirectResponse:
    conn.execute("UPDATE uploads SET archived_at = CURRENT_TIMESTAMP WHERE id = ?", (upload_id,))
    return RedirectResponse("/admin", status_code=303)


@app.post("/admin/uploads/{upload_id}/delete")
def delete_upload(
    upload_id: int,
    _: None = Depends(require_admin),
    conn: sqlite3.Connection = Depends(get_conn),
) -> RedirectResponse:
    upload = row(conn, "SELECT pdf_path FROM uploads WHERE id = ?", (upload_id,))
    if upload:
        Path(upload["pdf_path"]).unlink(missing_ok=True)
        conn.execute("DELETE FROM uploads WHERE id = ?", (upload_id,))
    return RedirectResponse("/admin", status_code=303)


@app.get("/admin/uploads/{upload_id}/download")
def download_upload(
    upload_id: int,
    _: None = Depends(require_admin),
    conn: sqlite3.Connection = Depends(get_conn),
) -> FileResponse:
    upload = row(conn, "SELECT * FROM uploads WHERE id = ?", (upload_id,))
    if not upload:
        raise HTTPException(status_code=404)
    filename = f"receipt-{upload['id']}-{upload['cardholder_name']}.pdf".replace("/", "-")
    return FileResponse(upload["pdf_path"], media_type="application/pdf", filename=filename)


@app.get("/upload/{token}", response_class=HTMLResponse)
def upload_page(
    token: str,
    request: Request,
    conn: sqlite3.Connection = Depends(get_conn),
) -> HTMLResponse:
    if not secrets.compare_digest(token, settings.upload_token):
        raise HTTPException(status_code=404)
    return render_upload_form(request, conn, token)


@app.post("/upload/{token}", response_class=HTMLResponse)
async def receive_upload(
    token: str,
    request: Request,
    cardholder_id: int = Form(...),
    store_id: int | None = Form(None),
    total: str = Form(...),
    purchase_location: str = Form(...),
    note: str = Form(""),
    files: list[UploadFile] = File(...),
    conn: sqlite3.Connection = Depends(get_conn),
) -> HTMLResponse:
    if not secrets.compare_digest(token, settings.upload_token):
        raise HTTPException(status_code=404)
    cardholder = row(conn, "SELECT * FROM cardholders WHERE id = ?", (cardholder_id,))
    store = row(conn, "SELECT * FROM stores WHERE id = ?", (store_id,)) if store_id else None
    if not cardholder:
        return render_upload_form(request, conn, token, "Please select a valid cardholder.", 400)
    try:
        pdf_path, pdf_size, original_filenames = await save_receipt_pdf(
            files,
            settings.upload_dir,
            settings.max_upload_bytes,
            settings.auto_install_imagemagick,
        )
    except (ValueError, ImageMagickUnavailable, RuntimeError) as exc:
        return render_upload_form(request, conn, token, str(exc), 400)
    conn.execute(
        """
        INSERT INTO uploads (
            cardholder_id, store_id, cardholder_name, store_name, total, purchase_location,
            note, original_filenames, pdf_path, pdf_size_bytes
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """,
        (
            cardholder_id,
            store_id,
            cardholder["name"],
            store["name"] if store else None,
            total.strip(),
            purchase_location.strip(),
            note.strip(),
            ", ".join(original_filenames),
            str(pdf_path),
            pdf_size,
        ),
    )
    return render_upload_form(request, conn, token, success="Receipt uploaded.")


def render_upload_form(
    request: Request,
    conn: sqlite3.Connection,
    token: str,
    error: str | None = None,
    status_code: int = 200,
    success: str | None = None,
) -> HTMLResponse:
    return render(
        request,
        "upload.html",
        {
            "token": token,
            "cardholders": rows(conn, "SELECT * FROM cardholders ORDER BY name COLLATE NOCASE"),
            "stores": rows(conn, "SELECT * FROM stores ORDER BY name COLLATE NOCASE"),
            "error": error,
            "success": success,
        },
        status_code,
    )
