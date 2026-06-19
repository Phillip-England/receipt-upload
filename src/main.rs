use anyhow::{Context, Result, bail};
use axum::{
    Form, Router,
    body::Body,
    extract::{DefaultBodyLimit, Multipart, Path, State},
    http::{HeaderMap, HeaderValue, StatusCode, header},
    response::{Html, IntoResponse, Redirect, Response},
    routing::{get, post},
};
use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD};
use clap::{Parser, Subcommand};
use hmac::{Hmac, Mac};
use image::codecs::jpeg::JpegEncoder;
use rand::{Rng, distr::Alphanumeric};
use rusqlite::{Connection, OptionalExtension, params};
use serde::Deserialize;
use sha2::Sha256;
use std::{
    collections::HashMap,
    env, fs,
    net::SocketAddr,
    path::{Path as FsPath, PathBuf},
    sync::{Arc, RwLock},
};
use subtle::ConstantTimeEq;
use time::{Duration, OffsetDateTime, format_description::well_known::Rfc3339};
use tokio::net::TcpListener;
use uuid::Uuid;

const SESSION_COOKIE: &str = "receipt-upload-session";
const BAN_HOURS: i64 = 24;
const MAX_FAILED_LOGINS: i64 = 3;
const MAX_IMAGE_DIMENSION: u32 = 1600;
const JPEG_QUALITY: u8 = 76;
const PDF_DPI: f32 = 150.0;

const STYLE_CSS: &str = include_str!("styles.css");

#[derive(Clone)]
struct AppState {
    settings: Arc<Settings>,
    upload_token: Arc<RwLock<String>>,
}

impl AppState {
    fn current_upload_token(&self) -> String {
        self.upload_token
            .read()
            .unwrap_or_else(|e| e.into_inner())
            .clone()
    }
}

#[derive(Clone, Debug)]
struct Settings {
    admin_username: String,
    admin_password: String,
    secret_key: String,
    upload_token: String,
    app_base_url: String,
    data_dir: PathBuf,
    upload_dir: PathBuf,
    max_upload_bytes: usize,
}

impl Settings {
    fn load() -> Result<Self> {
        let saved = load_saved_config()?;
        let data_dir = PathBuf::from(config_value(&saved, "DATA_DIR"))
            .canonicalize()
            .unwrap_or_else(|_| {
                PathBuf::from(config_value(&saved, "DATA_DIR"))
                    .components()
                    .collect()
            });
        let max_upload_mb: usize = config_value(&saved, "MAX_UPLOAD_MB").parse().unwrap_or(50);
        Ok(Self {
            admin_username: config_value(&saved, "ADMIN_USERNAME"),
            admin_password: config_value(&saved, "ADMIN_PASSWORD"),
            secret_key: config_value(&saved, "SECRET_KEY"),
            upload_token: config_value(&saved, "UPLOAD_TOKEN"),
            app_base_url: config_value(&saved, "APP_BASE_URL"),
            upload_dir: data_dir.join("receipts"),
            data_dir,
            max_upload_bytes: max_upload_mb * 1024 * 1024,
        })
    }

    fn db_path(&self) -> PathBuf {
        self.data_dir.join("app.sqlite3")
    }

    fn upload_url(&self, upload_token: &str) -> String {
        format!(
            "{}/upload/{}",
            self.app_base_url.trim_end_matches('/'),
            upload_token
        )
    }

    fn uses_default_admin_credentials(&self) -> bool {
        self.admin_username == "admin" || self.admin_password == "password"
    }
}

#[derive(Parser)]
#[command(
    name = "receipt-upload",
    version,
    about = "Run the receipt upload portal."
)]
struct Cli {
    #[command(subcommand)]
    command: Option<Commands>,
}

#[derive(Subcommand)]
enum Commands {
    Serve {
        #[arg(long, default_value = "0.0.0.0")]
        host: String,
        #[arg(long, default_value_t = 8725)]
        port: u16,
    },
    GenerateSecretKey {
        #[arg(long, default_value_t = 48)]
        length: usize,
        #[arg(long)]
        raw: bool,
    },
    GenerateUploadToken {
        #[arg(long, default_value_t = 32)]
        length: usize,
        #[arg(long)]
        raw: bool,
    },
    SetUsername {
        username: String,
    },
    SetPassword {
        password: Option<String>,
    },
    SetConfig {
        name: String,
        value: String,
    },
    ListBannedIps {
        #[arg(long)]
        all: bool,
    },
    UnbanIp {
        id: i64,
    },
}

#[tokio::main]
async fn main() -> Result<()> {
    let args = normalize_args();
    let cli = Cli::parse_from(args);
    match cli.command.unwrap_or(Commands::Serve {
        host: "0.0.0.0".into(),
        port: 8725,
    }) {
        Commands::Serve { host, port } => serve(host, port).await,
        Commands::GenerateSecretKey { length, raw } => print_generated("SECRET_KEY", length, raw),
        Commands::GenerateUploadToken { length, raw } => {
            print_generated("UPLOAD_TOKEN", length, raw)
        }
        Commands::SetUsername { username } => save_config_value("ADMIN_USERNAME", &username)
            .map(|p| println!("Saved default admin username to {}.", p.display())),
        Commands::SetPassword { password } => {
            let password = match password {
                Some(value) => value,
                None => prompt_password()?,
            };
            save_config_value("ADMIN_PASSWORD", &password)
                .map(|p| println!("Saved default admin password to {}.", p.display()))
        }
        Commands::SetConfig { name, value } => save_config_value(&name, &value)
            .map(|p| println!("Saved default {name} to {}.", p.display())),
        Commands::ListBannedIps { all } => list_banned_ips(all),
        Commands::UnbanIp { id } => unban_ip(id),
    }
}

fn normalize_args() -> Vec<String> {
    let mut args: Vec<String> = env::args().collect();
    if args.len() > 1 && args[1].starts_with("--") && args[1] != "--help" {
        args.insert(1, "serve".to_string());
    }
    args
}

async fn serve(host: String, port: u16) -> Result<()> {
    let settings = Arc::new(Settings::load()?);
    fs::create_dir_all(&settings.upload_dir)?;
    init_db(&settings.db_path())?;
    let upload_token = load_upload_token(&settings)?;
    let max_body_bytes = settings.max_upload_bytes + 1024 * 1024;
    let state = AppState {
        settings,
        upload_token: Arc::new(RwLock::new(upload_token)),
    };
    let app = Router::new()
        .route("/", get(root))
        .route("/static/styles.css", get(styles))
        .route("/admin/login", get(login_page).post(login))
        .route("/admin/logout", post(logout))
        .route("/admin", get(admin_dashboard))
        .route("/admin/upload-link", post(update_upload_link))
        .route("/admin/cardholders", post(add_cardholder))
        .route("/admin/cardholders/{id}/delete", post(delete_cardholder))
        .route("/admin/stores", post(add_store))
        .route("/admin/stores/{id}/delete", post(delete_store))
        .route("/admin/uploads/{id}/archive", post(archive_upload))
        .route("/admin/uploads/{id}/delete", post(delete_upload))
        .route("/admin/uploads/{id}/download", get(download_upload))
        .route("/upload/{token}", get(upload_page).post(receive_upload))
        .layer(DefaultBodyLimit::max(max_body_bytes))
        .with_state(state);
    let addr: SocketAddr = format!("{host}:{port}").parse()?;
    let listener = TcpListener::bind(addr).await?;
    println!("receipt-upload listening on http://{addr}");
    axum::serve(listener, app)
        .with_graceful_shutdown(shutdown_signal())
        .await?;
    Ok(())
}

async fn shutdown_signal() {
    let _ = tokio::signal::ctrl_c().await;
}

async fn root() -> Redirect {
    Redirect::to("/admin/login")
}

async fn styles() -> impl IntoResponse {
    (
        [(header::CONTENT_TYPE, "text/css; charset=utf-8")],
        STYLE_CSS,
    )
}

async fn login_page(State(state): State<AppState>) -> Html<String> {
    Html(render_login(&state.settings, None))
}

#[derive(Deserialize)]
struct LoginForm {
    username: String,
    password: String,
}

async fn login(
    State(state): State<AppState>,
    headers: HeaderMap,
    Form(form): Form<LoginForm>,
) -> Response {
    let ip = client_ip(&headers);
    let db = state.settings.db_path();
    let result = with_conn(&db, |conn| {
        purge_old_login_attempts(conn)?;
        if is_ip_banned(conn, &ip)? {
            return Ok(LoginResult::Banned);
        }
        let valid = ct_eq(&form.username, &state.settings.admin_username)
            && ct_eq(&form.password, &state.settings.admin_password);
        if valid {
            clear_login_attempts(conn, &ip)?;
            Ok(LoginResult::Ok)
        } else {
            record_failed_login(conn, &ip)?;
            Ok(LoginResult::Invalid)
        }
    });
    match result {
        Ok(LoginResult::Ok) => {
            let cookie = sign_session(&state.settings.secret_key);
            let mut response = Redirect::to("/admin").into_response();
            response
                .headers_mut()
                .insert(header::SET_COOKIE, HeaderValue::from_str(&cookie).unwrap());
            response
        }
        Ok(LoginResult::Banned) => (
            StatusCode::TOO_MANY_REQUESTS,
            Html(render_login(
                &state.settings,
                Some("Too many failed attempts. Try again later."),
            )),
        )
            .into_response(),
        _ => (
            StatusCode::UNAUTHORIZED,
            Html(render_login(
                &state.settings,
                Some("Invalid username or password."),
            )),
        )
            .into_response(),
    }
}

enum LoginResult {
    Ok,
    Invalid,
    Banned,
}

async fn logout() -> Response {
    let mut response = Redirect::to("/admin/login").into_response();
    response.headers_mut().insert(
        header::SET_COOKIE,
        HeaderValue::from_static(
            "receipt-upload-session=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax",
        ),
    );
    response
}

async fn admin_dashboard(State(state): State<AppState>, headers: HeaderMap) -> Response {
    if !is_admin(&headers, &state.settings) {
        return redirect_login();
    }
    match load_admin_view(&state.settings) {
        Ok(view) => Html(render_admin(
            &state.settings,
            &state.current_upload_token(),
            &view,
            None,
        ))
        .into_response(),
        Err(err) => server_error(err),
    }
}

#[derive(Deserialize)]
struct UploadLinkForm {
    secret_code: String,
}

async fn update_upload_link(
    State(state): State<AppState>,
    headers: HeaderMap,
    Form(form): Form<UploadLinkForm>,
) -> Response {
    if !is_admin(&headers, &state.settings) {
        return redirect_login();
    }
    let secret_code = form.secret_code.trim();
    if let Err(error) = validate_upload_token(secret_code) {
        return match load_admin_view(&state.settings) {
            Ok(view) => (
                StatusCode::BAD_REQUEST,
                Html(render_admin(
                    &state.settings,
                    &state.current_upload_token(),
                    &view,
                    Some(error),
                )),
            )
                .into_response(),
            Err(err) => server_error(err),
        };
    }
    if let Err(err) = save_upload_token(&state.settings, secret_code) {
        return server_error(err);
    }
    *state
        .upload_token
        .write()
        .unwrap_or_else(|e| e.into_inner()) = secret_code.to_string();
    Redirect::to("/admin").into_response()
}

#[derive(Deserialize)]
struct NameForm {
    name: String,
}

async fn add_cardholder(
    State(state): State<AppState>,
    headers: HeaderMap,
    Form(form): Form<NameForm>,
) -> Response {
    if !is_admin(&headers, &state.settings) {
        return redirect_login();
    }
    let name = form.name.trim();
    if !name.is_empty()
        && let Err(err) = with_conn(&state.settings.db_path(), |conn| {
            conn.execute(
                "INSERT OR IGNORE INTO cardholders (name) VALUES (?)",
                [name],
            )?;
            Ok(())
        })
    {
        return server_error(err);
    }
    Redirect::to("/admin").into_response()
}

async fn delete_cardholder(
    State(state): State<AppState>,
    headers: HeaderMap,
    Path(id): Path<i64>,
) -> Response {
    if !is_admin(&headers, &state.settings) {
        return redirect_login();
    }
    if let Err(err) = with_conn(&state.settings.db_path(), |conn| {
        conn.execute("DELETE FROM cardholders WHERE id = ?", [id])?;
        Ok(())
    }) {
        return server_error(err);
    }
    Redirect::to("/admin").into_response()
}

async fn add_store(
    State(state): State<AppState>,
    headers: HeaderMap,
    Form(form): Form<NameForm>,
) -> Response {
    if !is_admin(&headers, &state.settings) {
        return redirect_login();
    }
    let name = form.name.trim();
    if !name.is_empty()
        && let Err(err) = with_conn(&state.settings.db_path(), |conn| {
            conn.execute("INSERT OR IGNORE INTO stores (name) VALUES (?)", [name])?;
            Ok(())
        })
    {
        return server_error(err);
    }
    Redirect::to("/admin").into_response()
}

async fn delete_store(
    State(state): State<AppState>,
    headers: HeaderMap,
    Path(id): Path<i64>,
) -> Response {
    if !is_admin(&headers, &state.settings) {
        return redirect_login();
    }
    if let Err(err) = with_conn(&state.settings.db_path(), |conn| {
        conn.execute("DELETE FROM stores WHERE id = ?", [id])?;
        Ok(())
    }) {
        return server_error(err);
    }
    Redirect::to("/admin").into_response()
}

async fn archive_upload(
    State(state): State<AppState>,
    headers: HeaderMap,
    Path(id): Path<i64>,
) -> Response {
    if !is_admin(&headers, &state.settings) {
        return redirect_login();
    }
    if let Err(err) = with_conn(&state.settings.db_path(), |conn| {
        conn.execute(
            "UPDATE uploads SET archived_at = ? WHERE id = ?",
            params![now_iso(), id],
        )?;
        Ok(())
    }) {
        return server_error(err);
    }
    Redirect::to("/admin").into_response()
}

async fn delete_upload(
    State(state): State<AppState>,
    headers: HeaderMap,
    Path(id): Path<i64>,
) -> Response {
    if !is_admin(&headers, &state.settings) {
        return redirect_login();
    }
    let result = with_conn(&state.settings.db_path(), |conn| {
        let pdf_path: Option<String> = conn
            .query_row("SELECT pdf_path FROM uploads WHERE id = ?", [id], |r| {
                r.get(0)
            })
            .optional()?;
        if let Some(pdf_path) = pdf_path {
            let _ = fs::remove_file(pdf_path);
            conn.execute("DELETE FROM uploads WHERE id = ?", [id])?;
        }
        Ok(())
    });
    if let Err(err) = result {
        return server_error(err);
    }
    Redirect::to("/admin").into_response()
}

async fn download_upload(
    State(state): State<AppState>,
    headers: HeaderMap,
    Path(id): Path<i64>,
) -> Response {
    if !is_admin(&headers, &state.settings) {
        return redirect_login();
    }
    let upload = match with_conn(&state.settings.db_path(), |conn| {
        Ok(conn
            .query_row(
                "SELECT id, cardholder_name, pdf_path FROM uploads WHERE id = ?",
                [id],
                |r| {
                    Ok((
                        r.get::<_, i64>(0)?,
                        r.get::<_, String>(1)?,
                        r.get::<_, String>(2)?,
                    ))
                },
            )
            .optional()?)
    }) {
        Ok(Some(upload)) => upload,
        Ok(None) => return StatusCode::NOT_FOUND.into_response(),
        Err(err) => return server_error(err),
    };
    let bytes = match fs::read(&upload.2) {
        Ok(bytes) => bytes,
        Err(_) => return StatusCode::NOT_FOUND.into_response(),
    };
    let filename = format!(
        "receipt-{}-{}.pdf",
        upload.0,
        upload.1.replace(['/', '\\'], "-")
    );
    let mut response = Response::new(Body::from(bytes));
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/pdf"),
    );
    response.headers_mut().insert(
        header::CONTENT_DISPOSITION,
        HeaderValue::from_str(&format!(
            "attachment; filename=\"{}\"",
            filename.replace('"', "")
        ))
        .unwrap(),
    );
    response
}

async fn upload_page(State(state): State<AppState>, Path(token): Path<String>) -> Response {
    if !ct_eq(&token, &state.current_upload_token()) {
        return StatusCode::NOT_FOUND.into_response();
    }
    match load_upload_options(&state.settings) {
        Ok(view) => Html(render_upload(&state.settings, &token, &view, None, None)).into_response(),
        Err(err) => server_error(err),
    }
}

async fn receive_upload(
    State(state): State<AppState>,
    Path(token): Path<String>,
    multipart: Multipart,
) -> Response {
    if !ct_eq(&token, &state.current_upload_token()) {
        return StatusCode::NOT_FOUND.into_response();
    }
    match handle_upload(&state.settings, multipart).await {
        Ok(()) => match load_upload_options(&state.settings) {
            Ok(view) => Html(render_upload(
                &state.settings,
                &token,
                &view,
                None,
                Some("Receipt uploaded."),
            ))
            .into_response(),
            Err(err) => server_error(err),
        },
        Err(err) => match load_upload_options(&state.settings) {
            Ok(view) => (
                StatusCode::BAD_REQUEST,
                Html(render_upload(
                    &state.settings,
                    &token,
                    &view,
                    Some(&err.to_string()),
                    None,
                )),
            )
                .into_response(),
            Err(load_err) => server_error(load_err),
        },
    }
}

async fn handle_upload(settings: &Settings, mut multipart: Multipart) -> Result<()> {
    let mut text = HashMap::<String, Vec<String>>::new();
    let mut images = Vec::<PdfImage>::new();
    let mut original_filenames = Vec::<String>::new();
    let mut total_bytes = 0usize;
    while let Some(field) = multipart.next_field().await? {
        let name = field.name().unwrap_or("").to_string();
        if name == "files" {
            let filename = field.file_name().unwrap_or("receipt").to_string();
            let bytes = field.bytes().await?;
            if bytes.is_empty() {
                continue;
            }
            total_bytes += bytes.len();
            if total_bytes > settings.max_upload_bytes {
                bail!("Upload is too large.");
            }
            let image = prepare_pdf_image(&bytes)
                .with_context(|| format!("Could not read {filename} as a receipt image"))?;
            images.push(image);
            original_filenames.push(filename);
        } else {
            let value = field.text().await.unwrap_or_default();
            text.entry(name).or_default().push(value);
        }
    }
    if images.is_empty() {
        bail!("Please choose at least one receipt image.");
    }
    let cardholder_id: i64 = required_text(&text, "cardholder_id")?
        .parse()
        .context("Please select a valid cardholder.")?;
    let total = required_text(&text, "total")?.trim().to_string();
    let purchase_location = required_text(&text, "purchase_location")?
        .trim()
        .to_string();
    let description = text
        .get("description")
        .and_then(|v| v.first())
        .map(|s| s.trim().to_string())
        .unwrap_or_default();
    let notes = text
        .get("notes")
        .and_then(|v| v.first())
        .map(|s| s.trim().to_string())
        .unwrap_or_default();
    let store_ids = text
        .get("store_ids")
        .cloned()
        .unwrap_or_default()
        .into_iter()
        .filter_map(|v| v.parse::<i64>().ok())
        .collect::<Vec<_>>();
    if total.is_empty() || purchase_location.is_empty() {
        bail!("Total and place of purchase are required.");
    }
    fs::create_dir_all(&settings.upload_dir)?;
    let output_path = settings.upload_dir.join(format!("{}.pdf", Uuid::new_v4()));
    write_pdf(&images, &output_path)?;
    let pdf_size = fs::metadata(&output_path)?.len() as i64;
    with_conn(&settings.db_path(), |conn| {
        let tx = conn.transaction()?;
        let cardholder: Option<String> = tx
            .query_row(
                "SELECT name FROM cardholders WHERE id = ?",
                [cardholder_id],
                |r| r.get(0),
            )
            .optional()?;
        let Some(cardholder_name) = cardholder else {
            bail!("Please select a valid cardholder.");
        };
        let store_names = if store_ids.is_empty() {
            Vec::new()
        } else {
            let mut names = Vec::new();
            for store_id in &store_ids {
                if let Some(name) = tx
                    .query_row("SELECT name FROM stores WHERE id = ?", [store_id], |r| {
                        r.get::<_, String>(0)
                    })
                    .optional()?
                {
                    names.push((*store_id, name));
                }
            }
            names
        };
        tx.execute(
            "INSERT INTO uploads (cardholder_id, cardholder_name, total, purchase_location, description, notes, store_names, original_filenames, pdf_path, pdf_size_bytes, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            params![cardholder_id, cardholder_name, total, purchase_location, description, notes, store_names.iter().map(|(_, n)| n.as_str()).collect::<Vec<_>>().join(", "), original_filenames.join(", "), output_path.to_string_lossy(), pdf_size, now_iso()],
        )?;
        let upload_id = tx.last_insert_rowid();
        for (store_id, store_name) in store_names {
            tx.execute(
                "INSERT INTO receipt_stores (upload_id, store_id, store_name) VALUES (?, ?, ?)",
                params![upload_id, store_id, store_name],
            )?;
        }
        tx.commit()?;
        Ok(())
    })?;
    Ok(())
}

fn required_text<'a>(text: &'a HashMap<String, Vec<String>>, key: &str) -> Result<&'a str> {
    text.get(key)
        .and_then(|v| v.first())
        .map(String::as_str)
        .filter(|v| !v.trim().is_empty())
        .with_context(|| format!("{key} is required."))
}

struct PdfImage {
    width: u32,
    height: u32,
    jpeg: Vec<u8>,
}

fn prepare_pdf_image(bytes: &[u8]) -> Result<PdfImage> {
    let image = image::load_from_memory(bytes)?;
    let resized = image
        .resize(
            MAX_IMAGE_DIMENSION,
            MAX_IMAGE_DIMENSION,
            image::imageops::FilterType::Triangle,
        )
        .to_rgb8();
    let mut jpeg = Vec::new();
    JpegEncoder::new_with_quality(&mut jpeg, JPEG_QUALITY).encode_image(&resized)?;
    Ok(PdfImage {
        width: resized.width(),
        height: resized.height(),
        jpeg,
    })
}

fn write_pdf(images: &[PdfImage], output_path: &FsPath) -> Result<()> {
    let pages_id = 1usize;
    let catalog_id = 2usize;
    let mut objects = Vec::<Vec<u8>>::new();
    objects.push(Vec::new());
    objects.push(Vec::new());
    let mut page_ids = Vec::new();
    for (index, image) in images.iter().enumerate() {
        let page_id = 3 + index * 3;
        let content_id = page_id + 1;
        let image_id = page_id + 2;
        page_ids.push(page_id);
        let width_pt = image.width as f32 * 72.0 / PDF_DPI;
        let height_pt = image.height as f32 * 72.0 / PDF_DPI;
        objects.push(format!("<< /Type /Page /Parent {pages_id} 0 R /MediaBox [0 0 {width_pt:.2} {height_pt:.2}] /Resources << /XObject << /Im{index} {image_id} 0 R >> >> /Contents {content_id} 0 R >>").into_bytes());
        let content = format!("q\n{width_pt:.2} 0 0 {height_pt:.2} 0 0 cm\n/Im{index} Do\nQ\n");
        objects.push(
            format!(
                "<< /Length {} >>\nstream\n{}endstream",
                content.len(),
                content
            )
            .into_bytes(),
        );
        let mut image_obj = format!("<< /Type /XObject /Subtype /Image /Width {} /Height {} /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length {} >>\nstream\n", image.width, image.height, image.jpeg.len()).into_bytes();
        image_obj.extend_from_slice(&image.jpeg);
        image_obj.extend_from_slice(b"\nendstream");
        objects.push(image_obj);
    }
    objects[pages_id - 1] = format!(
        "<< /Type /Pages /Kids [{}] /Count {} >>",
        page_ids
            .iter()
            .map(|id| format!("{id} 0 R"))
            .collect::<Vec<_>>()
            .join(" "),
        page_ids.len()
    )
    .into_bytes();
    objects[catalog_id - 1] = format!("<< /Type /Catalog /Pages {pages_id} 0 R >>").into_bytes();
    let mut pdf = b"%PDF-1.4\n%\xE2\xE3\xCF\xD3\n".to_vec();
    let mut offsets = Vec::new();
    for (idx, obj) in objects.iter().enumerate() {
        offsets.push(pdf.len());
        pdf.extend_from_slice(format!("{} 0 obj\n", idx + 1).as_bytes());
        pdf.extend_from_slice(obj);
        pdf.extend_from_slice(b"\nendobj\n");
    }
    let xref_at = pdf.len();
    pdf.extend_from_slice(
        format!("xref\n0 {}\n0000000000 65535 f \n", objects.len() + 1).as_bytes(),
    );
    for offset in offsets {
        pdf.extend_from_slice(format!("{offset:010} 00000 n \n").as_bytes());
    }
    pdf.extend_from_slice(
        format!(
            "trailer\n<< /Size {} /Root {catalog_id} 0 R >>\nstartxref\n{xref_at}\n%%EOF\n",
            objects.len() + 1
        )
        .as_bytes(),
    );
    fs::write(output_path, pdf)?;
    Ok(())
}

fn init_db(db_path: &FsPath) -> Result<()> {
    if let Some(parent) = db_path.parent() {
        fs::create_dir_all(parent)?;
    }
    with_conn(db_path, |conn| {
        conn.execute_batch(
            "
            PRAGMA journal_mode = WAL;
            PRAGMA foreign_keys = ON;
            CREATE TABLE IF NOT EXISTS cardholders (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                name TEXT NOT NULL UNIQUE,
                created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
            );
            CREATE TABLE IF NOT EXISTS stores (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                name TEXT NOT NULL UNIQUE,
                created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
            );
            CREATE TABLE IF NOT EXISTS uploads (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                cardholder_id INTEGER,
                cardholder_name TEXT NOT NULL,
                total TEXT NOT NULL,
                purchase_location TEXT NOT NULL,
                description TEXT,
                notes TEXT,
                store_names TEXT,
                original_filenames TEXT NOT NULL,
                pdf_path TEXT NOT NULL,
                pdf_size_bytes INTEGER NOT NULL,
                archived_at TEXT,
                created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                FOREIGN KEY(cardholder_id) REFERENCES cardholders(id) ON DELETE SET NULL
            );
            CREATE TABLE IF NOT EXISTS receipt_stores (
                upload_id INTEGER NOT NULL,
                store_id INTEGER,
                store_name TEXT NOT NULL,
                PRIMARY KEY(upload_id, store_name),
                FOREIGN KEY(upload_id) REFERENCES uploads(id) ON DELETE CASCADE,
                FOREIGN KEY(store_id) REFERENCES stores(id) ON DELETE SET NULL
            );
            CREATE TABLE IF NOT EXISTS login_attempts (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                ip_address TEXT NOT NULL UNIQUE,
                failed_count INTEGER NOT NULL,
                last_attempt_at TEXT NOT NULL,
                banned_until TEXT
            );
            CREATE TABLE IF NOT EXISTS app_settings (
                name TEXT PRIMARY KEY,
                value TEXT NOT NULL
            );
            ",
        )?;
        add_column_if_missing(conn, "uploads", "description", "TEXT")?;
        add_column_if_missing(conn, "uploads", "notes", "TEXT")?;
        add_column_if_missing(conn, "uploads", "store_names", "TEXT")?;
        migrate_login_attempt_ids(conn)?;
        Ok(())
    })
}

fn load_upload_token(settings: &Settings) -> Result<String> {
    with_conn(&settings.db_path(), |conn| {
        Ok(conn
            .query_row(
                "SELECT value FROM app_settings WHERE name = 'UPLOAD_TOKEN'",
                [],
                |row| row.get(0),
            )
            .optional()?
            .unwrap_or_else(|| settings.upload_token.clone()))
    })
}

fn save_upload_token(settings: &Settings, upload_token: &str) -> Result<()> {
    with_conn(&settings.db_path(), |conn| {
        conn.execute(
            "INSERT INTO app_settings (name, value) VALUES ('UPLOAD_TOKEN', ?) \
             ON CONFLICT(name) DO UPDATE SET value = excluded.value",
            [upload_token],
        )?;
        Ok(())
    })
}

fn validate_upload_token(upload_token: &str) -> std::result::Result<(), &'static str> {
    if !(8..=128).contains(&upload_token.len()) {
        return Err("The secret code must be between 8 and 128 characters.");
    }
    if !upload_token
        .bytes()
        .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_'))
    {
        return Err("Use only letters, numbers, hyphens, and underscores in the secret code.");
    }
    Ok(())
}

fn add_column_if_missing(conn: &Connection, table: &str, column: &str, ty: &str) -> Result<()> {
    let mut stmt = conn.prepare(&format!("PRAGMA table_info({table})"))?;
    let exists = stmt
        .query_map([], |row| row.get::<_, String>(1))?
        .filter_map(Result::ok)
        .any(|name| name == column);
    if !exists {
        conn.execute(&format!("ALTER TABLE {table} ADD COLUMN {column} {ty}"), [])?;
    }
    Ok(())
}

fn migrate_login_attempt_ids(conn: &Connection) -> Result<()> {
    let mut stmt = conn.prepare("PRAGMA table_info(login_attempts)")?;
    let has_id = stmt
        .query_map([], |row| row.get::<_, String>(1))?
        .filter_map(Result::ok)
        .any(|name| name == "id");
    if !has_id {
        conn.execute_batch(
            "
            ALTER TABLE login_attempts RENAME TO login_attempts_old;
            CREATE TABLE login_attempts (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                ip_address TEXT NOT NULL UNIQUE,
                failed_count INTEGER NOT NULL,
                last_attempt_at TEXT NOT NULL,
                banned_until TEXT
            );
            INSERT INTO login_attempts (ip_address, failed_count, last_attempt_at, banned_until)
            SELECT ip_address, failed_count, last_attempt_at, banned_until FROM login_attempts_old;
            DROP TABLE login_attempts_old;
            ",
        )?;
    }
    Ok(())
}

fn with_conn<T>(db_path: &FsPath, f: impl FnOnce(&mut Connection) -> Result<T>) -> Result<T> {
    let mut conn = Connection::open(db_path)?;
    conn.execute_batch("PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;")?;
    f(&mut conn)
}

#[derive(Debug)]
struct AdminView {
    cardholders: Vec<NamedRow>,
    stores: Vec<NamedRow>,
    uploads: Vec<UploadRow>,
    disk_usage: String,
}

#[derive(Debug)]
struct UploadOptions {
    cardholders: Vec<NamedRow>,
    stores: Vec<NamedRow>,
}

#[derive(Debug)]
struct NamedRow {
    id: i64,
    name: String,
}

#[derive(Debug)]
struct UploadRow {
    id: i64,
    cardholder_name: String,
    total: String,
    purchase_location: String,
    description: String,
    notes: String,
    store_names: String,
    pdf_size: String,
    archived_at: Option<String>,
    created_at: String,
}

fn load_admin_view(settings: &Settings) -> Result<AdminView> {
    with_conn(&settings.db_path(), |conn| {
        Ok(AdminView {
            cardholders: named_rows(
                conn,
                "SELECT id, name FROM cardholders ORDER BY name COLLATE NOCASE",
            )?,
            stores: named_rows(
                conn,
                "SELECT id, name FROM stores ORDER BY name COLLATE NOCASE",
            )?,
            uploads: upload_rows(conn)?,
            disk_usage: human_bytes(directory_size(&settings.data_dir)),
        })
    })
}

fn load_upload_options(settings: &Settings) -> Result<UploadOptions> {
    with_conn(&settings.db_path(), |conn| {
        Ok(UploadOptions {
            cardholders: named_rows(
                conn,
                "SELECT id, name FROM cardholders ORDER BY name COLLATE NOCASE",
            )?,
            stores: named_rows(
                conn,
                "SELECT id, name FROM stores ORDER BY name COLLATE NOCASE",
            )?,
        })
    })
}

fn named_rows(conn: &Connection, query: &str) -> Result<Vec<NamedRow>> {
    Ok(conn
        .prepare(query)?
        .query_map([], |r| {
            Ok(NamedRow {
                id: r.get(0)?,
                name: r.get(1)?,
            })
        })?
        .collect::<std::result::Result<Vec<_>, _>>()?)
}

fn upload_rows(conn: &Connection) -> Result<Vec<UploadRow>> {
    Ok(conn.prepare(
        "SELECT id, cardholder_name, total, purchase_location, COALESCE(description, ''), COALESCE(notes, ''), COALESCE(store_names, ''), pdf_size_bytes, archived_at, created_at FROM uploads ORDER BY archived_at IS NOT NULL, created_at DESC",
    )?.query_map([], |r| {
        let size: i64 = r.get(7)?;
        Ok(UploadRow {
            id: r.get(0)?,
            cardholder_name: r.get(1)?,
            total: r.get(2)?,
            purchase_location: r.get(3)?,
            description: r.get(4)?,
            notes: r.get(5)?,
            store_names: r.get(6)?,
            pdf_size: human_bytes(size as u64),
            archived_at: r.get(8)?,
            created_at: r.get(9)?,
        })
    })?.collect::<std::result::Result<Vec<_>, _>>()?)
}

fn render_login(settings: &Settings, error: Option<&str>) -> String {
    layout(
        "Login",
        &format!(
            r#"<main class="auth-shell"><section class="auth-panel"><h1>receipt-upload</h1>{warning}{error}<form method="post" action="/admin/login" class="stack"><label>Username <input name="username" autocomplete="username" required autofocus></label><label>Password <input name="password" type="password" autocomplete="current-password" required></label><button type="submit">Log in</button></form></section></main>"#,
            warning = default_warning(settings),
            error = message("alert", error),
        ),
    )
}

fn render_admin(
    settings: &Settings,
    upload_token: &str,
    view: &AdminView,
    upload_link_error: Option<&str>,
) -> String {
    let cardholders = if view.cardholders.is_empty() {
        r#"<li class="empty">No cardholders yet.</li>"#.to_string()
    } else {
        view.cardholders.iter().map(|r| format!(r#"<li><span>{}</span><form method="post" action="/admin/cardholders/{}/delete"><button class="danger secondary" type="submit">Remove</button></form></li>"#, esc(&r.name), r.id)).collect()
    };
    let stores = if view.stores.is_empty() {
        r#"<li class="empty">No stores yet.</li>"#.to_string()
    } else {
        view.stores.iter().map(|r| format!(r#"<li><span>{}</span><form method="post" action="/admin/stores/{}/delete"><button class="danger secondary" type="submit">Remove</button></form></li>"#, esc(&r.name), r.id)).collect()
    };
    let uploads = if view.uploads.is_empty() {
        r#"<tr><td colspan="9" class="empty">No receipts uploaded yet.</td></tr>"#.to_string()
    } else {
        view.uploads.iter().map(|u| format!(
            r#"<tr class="{muted}"><td>{created}</td><td>{cardholder}</td><td>{total}</td><td>{place}</td><td>{stores}</td><td>{description}</td><td>{size}</td><td>{status}</td><td class="actions"><a class="button secondary" href="/admin/uploads/{id}/download" data-download-link data-loading-text="Preparing PDF...">Download</a>{archive}<form method="post" action="/admin/uploads/{id}/delete"><button class="danger secondary" type="submit">Delete</button></form></td></tr>"#,
            muted = if u.archived_at.is_some() { "muted" } else { "" },
            created = esc(&u.created_at),
            cardholder = esc(&u.cardholder_name),
            total = esc(&u.total),
            place = esc(&u.purchase_location),
            stores = if u.store_names.is_empty() { "Unassigned".to_string() } else { esc(&u.store_names) },
            description = esc(if u.description.is_empty() { &u.notes } else { &u.description }),
            size = esc(&u.pdf_size),
            status = if u.archived_at.is_some() { "Archived" } else { "Active" },
            id = u.id,
            archive = if u.archived_at.is_none() { format!(r#"<form method="post" action="/admin/uploads/{}/archive"><button class="secondary" type="submit">Archive</button></form>"#, u.id) } else { String::new() },
        )).collect()
    };
    layout(
        "Admin",
        &format!(
            r#"<header class="topbar"><div><h1>receipt-upload</h1><p>{disk_usage} used by this app</p></div><form method="post" action="/admin/logout"><button class="secondary" type="submit">Log out</button></form></header><main class="admin-grid"><section class="wide">{warning}</section><section class="panel wide"><h2>Secret Upload Link</h2>{upload_link_error}<div class="copy-row"><input readonly value="{upload_url}" aria-label="Current secret upload link"><a class="button" href="{upload_url}" target="_blank">Open</a></div><form class="secret-code-form" method="post" action="/admin/upload-link"><label>Secret code <input name="secret_code" value="{upload_token}" minlength="8" maxlength="128" pattern="[A-Za-z0-9_-]+" autocomplete="off" required></label><button type="submit">Change link</button></form><p class="help-text">Changing the code immediately disables the old upload link.</p></section><section class="panel"><h2>Cardholders</h2><form class="inline-form" method="post" action="/admin/cardholders"><input name="name" placeholder="Name" required><button type="submit">Add</button></form><ul class="manage-list">{cardholders}</ul></section><section class="panel"><h2>Stores</h2><form class="inline-form" method="post" action="/admin/stores"><input name="name" placeholder="Store" required><button type="submit">Add</button></form><ul class="manage-list">{stores}</ul></section><section class="panel wide"><h2>Uploads</h2><div class="table-wrap"><table><thead><tr><th>Date</th><th>Cardholder</th><th>Total</th><th>Purchased At</th><th>Stores</th><th>Description</th><th>PDF</th><th>Status</th><th>Actions</th></tr></thead><tbody>{uploads}</tbody></table></div></section></main>"#,
            disk_usage = view.disk_usage,
            warning = default_warning(settings),
            upload_link_error = message("alert", upload_link_error),
            upload_url = esc(&settings.upload_url(upload_token)),
            upload_token = esc(upload_token),
            cardholders = cardholders,
            stores = stores,
            uploads = uploads,
        ),
    )
}

fn render_upload(
    _settings: &Settings,
    token: &str,
    view: &UploadOptions,
    error: Option<&str>,
    success: Option<&str>,
) -> String {
    let cardholders: String = view
        .cardholders
        .iter()
        .map(|r| format!(r#"<option value="{}">{}</option>"#, r.id, esc(&r.name)))
        .collect();
    let stores = if view.stores.is_empty() {
        r#"<p class="empty left">No stores have been configured.</p>"#.to_string()
    } else {
        view.stores.iter().map(|s| format!(r#"<label class="check-row"><input type="checkbox" name="store_ids" value="{}"><span>{}</span></label>"#, s.id, esc(&s.name))).collect()
    };
    layout(
        "Upload Receipt",
        &format!(
            r#"<main class="upload-shell"><section class="upload-panel"><h1>Upload Receipt</h1>{success}{error}<form method="post" action="/upload/{token}" enctype="multipart/form-data" class="stack" data-loading-form data-resize-upload><label>Cardholder<select name="cardholder_id" required><option value="">Select a name</option>{cardholders}</select></label><label>Total <input name="total" inputmode="decimal" placeholder="42.50" required></label><label>Place of Purchase <input name="purchase_location" placeholder="Vendor or location" required></label><label>Description <input name="description" placeholder="Business purpose or expense label"></label><fieldset><legend>Stores</legend><div class="check-list">{stores}</div></fieldset><label>Notes <textarea name="notes" rows="4"></textarea></label><label>Receipt Images<input name="files" type="file" multiple accept="image/*" required data-append-files data-file-list="receipt-file-list"></label><div class="file-selection" aria-live="polite"><div class="file-selection-header"><span id="receipt-file-count">No files selected</span><button class="secondary" type="button" data-clear-files>Clear</button></div><ul id="receipt-file-list" class="selected-files"></ul></div><button type="submit" data-loading-text="Uploading...">Upload</button><div class="loading-status" role="status" aria-live="polite"><span class="spinner" aria-hidden="true"></span><span>Uploading receipt.</span></div></form></section></main>"#,
            token = esc(token),
            success = message("success", success),
            error = message("alert", error),
            cardholders = cardholders,
            stores = stores,
        ),
    )
}

fn layout(title: &str, body: &str) -> String {
    format!(
        r#"<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{}</title><link rel="stylesheet" href="/static/styles.css"></head><body>{}<script>{}</script></body></html>"#,
        esc(title),
        body,
        CLIENT_JS,
    )
}

const CLIENT_JS: &str = r#"
document.querySelectorAll("[data-loading-form]").forEach((form)=>{form.addEventListener("submit",()=>{form.classList.add("is-loading");form.setAttribute("aria-busy","true");form.querySelectorAll("button[type='submit']").forEach((button)=>{button.dataset.originalText=button.textContent;button.textContent=button.dataset.loadingText||"Working...";button.disabled=true;});});});
document.querySelectorAll("[data-append-files]").forEach((input)=>{const form=input.closest("form");const fileList=document.getElementById(input.dataset.fileList);const fileCount=document.getElementById("receipt-file-count");const clearButton=form?form.querySelector("[data-clear-files]"):null;let selectedFiles=window.DataTransfer?new DataTransfer():null;const renderFiles=()=>{const files=selectedFiles?selectedFiles.files:input.files;if(fileCount){const count=files.length;fileCount.textContent=count===0?"No files selected":count===1?"1 file selected":`${count} files selected`;}if(!fileList)return;fileList.innerHTML="";Array.from(files).forEach((file)=>{const item=document.createElement("li");item.textContent=`${file.name} (${Math.max(1,Math.round(file.size/1024))} KB)`;fileList.appendChild(item);});};input.addEventListener("change",()=>{if(selectedFiles){Array.from(input.files).forEach((file)=>selectedFiles.items.add(file));input.files=selectedFiles.files;}renderFiles();});if(clearButton){clearButton.addEventListener("click",()=>{if(window.DataTransfer){selectedFiles=new DataTransfer();input.files=selectedFiles.files;}else{input.value="";}renderFiles();});}renderFiles();});
document.querySelectorAll("[data-resize-upload]").forEach((form)=>{form.addEventListener("submit",async(event)=>{const input=form.querySelector("input[type='file'][name='files']");if(!input||!window.DataTransfer||input.dataset.resized==="true")return;event.preventDefault();const dt=new DataTransfer();for(const file of Array.from(input.files)){if(!file.type.startsWith("image/")){dt.items.add(file);continue;}const resized=await resizeImage(file,1600,0.76);dt.items.add(resized);}input.files=dt.files;input.dataset.resized="true";form.requestSubmit();});});
async function resizeImage(file,maxDim,quality){const bitmap=await createImageBitmap(file);const scale=Math.min(1,maxDim/Math.max(bitmap.width,bitmap.height));const width=Math.max(1,Math.round(bitmap.width*scale));const height=Math.max(1,Math.round(bitmap.height*scale));const canvas=document.createElement("canvas");canvas.width=width;canvas.height=height;canvas.getContext("2d").drawImage(bitmap,0,0,width,height);const blob=await new Promise((resolve)=>canvas.toBlob(resolve,"image/jpeg",quality));return new File([blob],file.name.replace(/\.[^.]+$/,"")+".jpg",{type:"image/jpeg",lastModified:file.lastModified});}
document.querySelectorAll("[data-download-link]").forEach((link)=>{link.addEventListener("click",()=>{if(link.classList.contains("is-loading"))return;link.dataset.originalText=link.textContent;link.textContent=link.dataset.loadingText||"Preparing...";link.classList.add("is-loading");link.setAttribute("aria-busy","true");window.setTimeout(()=>{link.textContent=link.dataset.originalText||"Download";link.classList.remove("is-loading");link.removeAttribute("aria-busy");},8000);});});
"#;

fn default_warning(settings: &Settings) -> String {
    if settings.uses_default_admin_credentials() {
        r#"<div class="security-warning">Default admin credentials are active. Change <code>ADMIN_USERNAME</code> and <code>ADMIN_PASSWORD</code> before real use.</div>"#.to_string()
    } else {
        String::new()
    }
}

fn message(class_name: &str, value: Option<&str>) -> String {
    value
        .map(|v| format!(r#"<p class="{class_name}">{}</p>"#, esc(v)))
        .unwrap_or_default()
}

fn esc(value: &str) -> String {
    html_escape::encode_text(value).into_owned()
}

fn redirect_login() -> Response {
    Redirect::to("/admin/login").into_response()
}

fn server_error(err: anyhow::Error) -> Response {
    eprintln!("{err:?}");
    (StatusCode::INTERNAL_SERVER_ERROR, "Internal server error").into_response()
}

fn sign_session(secret_key: &str) -> String {
    let body = URL_SAFE_NO_PAD.encode(r#"{"admin":true}"#);
    let mut mac = Hmac::<Sha256>::new_from_slice(secret_key.as_bytes()).unwrap();
    mac.update(body.as_bytes());
    let sig = hex(&mac.finalize().into_bytes());
    format!("{SESSION_COOKIE}={body}.{sig}; Path=/; HttpOnly; SameSite=Lax")
}

fn is_admin(headers: &HeaderMap, settings: &Settings) -> bool {
    let Some(cookie) = headers.get(header::COOKIE).and_then(|v| v.to_str().ok()) else {
        return false;
    };
    let Some(value) = cookie
        .split(';')
        .map(str::trim)
        .find_map(|part| part.strip_prefix(&format!("{SESSION_COOKIE}=")))
    else {
        return false;
    };
    let Some((body, sig)) = value.rsplit_once('.') else {
        return false;
    };
    let mut mac = Hmac::<Sha256>::new_from_slice(settings.secret_key.as_bytes()).unwrap();
    mac.update(body.as_bytes());
    let expected = hex(&mac.finalize().into_bytes());
    if !ct_eq(sig, &expected) {
        return false;
    }
    URL_SAFE_NO_PAD
        .decode(body)
        .ok()
        .and_then(|bytes| String::from_utf8(bytes).ok())
        .as_deref()
        == Some(r#"{"admin":true}"#)
}

fn ct_eq(a: &str, b: &str) -> bool {
    a.as_bytes().ct_eq(b.as_bytes()).into()
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

fn client_ip(headers: &HeaderMap) -> String {
    headers
        .get("x-forwarded-for")
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.split(',').next())
        .map(str::trim)
        .filter(|v| !v.is_empty())
        .unwrap_or("unknown")
        .to_string()
}

fn now_iso() -> String {
    OffsetDateTime::now_utc().format(&Rfc3339).unwrap()
}

fn parse_iso(value: Option<String>) -> Option<OffsetDateTime> {
    value.and_then(|v| OffsetDateTime::parse(&v, &Rfc3339).ok())
}

fn purge_old_login_attempts(conn: &Connection) -> Result<()> {
    let cutoff = (OffsetDateTime::now_utc() - Duration::hours(BAN_HOURS)).format(&Rfc3339)?;
    let now = now_iso();
    conn.execute("DELETE FROM login_attempts WHERE last_attempt_at < ? AND (banned_until IS NULL OR banned_until < ?)", params![cutoff, now])?;
    Ok(())
}

fn is_ip_banned(conn: &Connection, ip: &str) -> Result<bool> {
    let banned_until: Option<String> = conn
        .query_row(
            "SELECT banned_until FROM login_attempts WHERE ip_address = ?",
            [ip],
            |r| r.get(0),
        )
        .optional()?;
    Ok(parse_iso(banned_until).is_some_and(|dt| dt > OffsetDateTime::now_utc()))
}

fn record_failed_login(conn: &Connection, ip: &str) -> Result<()> {
    let failed_count: Option<i64> = conn
        .query_row(
            "SELECT failed_count FROM login_attempts WHERE ip_address = ?",
            [ip],
            |r| r.get(0),
        )
        .optional()?;
    let failed_count = failed_count.unwrap_or(0) + 1;
    let banned_until = if failed_count >= MAX_FAILED_LOGINS {
        Some((OffsetDateTime::now_utc() + Duration::hours(BAN_HOURS)).format(&Rfc3339)?)
    } else {
        None
    };
    conn.execute(
        "INSERT INTO login_attempts (ip_address, failed_count, last_attempt_at, banned_until) VALUES (?, ?, ?, ?) ON CONFLICT(ip_address) DO UPDATE SET failed_count = excluded.failed_count, last_attempt_at = excluded.last_attempt_at, banned_until = excluded.banned_until",
        params![ip, failed_count, now_iso(), banned_until],
    )?;
    Ok(())
}

fn clear_login_attempts(conn: &Connection, ip: &str) -> Result<()> {
    conn.execute("DELETE FROM login_attempts WHERE ip_address = ?", [ip])?;
    Ok(())
}

fn directory_size(path: &FsPath) -> u64 {
    let mut total = 0;
    if let Ok(entries) = fs::read_dir(path) {
        for entry in entries.flatten() {
            let path = entry.path();
            if path.is_dir() {
                total += directory_size(&path);
            } else if let Ok(meta) = entry.metadata() {
                total += meta.len();
            }
        }
    }
    total
}

fn human_bytes(size: u64) -> String {
    let units = ["B", "KB", "MB", "GB", "TB"];
    let mut value = size as f64;
    for unit in units {
        if value < 1024.0 || unit == "TB" {
            return if unit == "B" {
                format!("{} B", size)
            } else {
                format!("{value:.1} {unit}")
            };
        }
        value /= 1024.0;
    }
    format!("{size} B")
}

fn default_env() -> HashMap<&'static str, &'static str> {
    HashMap::from([
        ("ADMIN_USERNAME", "admin"),
        ("ADMIN_PASSWORD", "password"),
        ("SECRET_KEY", "dev-secret-change-me"),
        ("UPLOAD_TOKEN", "dev-upload-token"),
        ("APP_BASE_URL", "http://localhost:8725"),
        ("DATA_DIR", "./data"),
        ("MAX_UPLOAD_MB", "50"),
    ])
}

fn config_path() -> PathBuf {
    if let Ok(path) = env::var("RECEIPT_UPLOAD_CONFIG") {
        return PathBuf::from(path);
    }
    dirs::config_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join("receipt-upload")
        .join("config.json")
}

fn load_saved_config() -> Result<HashMap<String, String>> {
    let path = config_path();
    if !path.exists() {
        return Ok(HashMap::new());
    }
    let values: HashMap<String, String> = serde_json::from_str(&fs::read_to_string(path)?)?;
    Ok(values)
}

fn config_value(saved: &HashMap<String, String>, name: &str) -> String {
    env::var(name)
        .ok()
        .or_else(|| saved.get(name).cloned())
        .unwrap_or_else(|| default_env()[name].to_string())
}

fn save_config_value(name: &str, value: &str) -> Result<PathBuf> {
    if !default_env().contains_key(name) {
        bail!("Unsupported configuration key: {name}");
    }
    if value.is_empty() {
        bail!("{name} cannot be empty.");
    }
    let mut values = load_saved_config()?;
    values.insert(name.to_string(), value.to_string());
    let path = config_path();
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }
    fs::write(&path, serde_json::to_string_pretty(&values)? + "\n")?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&path, fs::Permissions::from_mode(0o600))?;
    }
    Ok(path)
}

fn print_generated(key: &str, length: usize, raw: bool) -> Result<()> {
    if length == 0 {
        bail!("Length must be greater than zero.");
    }
    let value: String = rand::rng()
        .sample_iter(&Alphanumeric)
        .take(length)
        .map(char::from)
        .collect();
    if raw {
        println!("{value}");
    } else {
        println!("export {key}='{value}'");
    }
    Ok(())
}

fn prompt_password() -> Result<String> {
    let password = rpassword::prompt_password("Admin password: ")?;
    if password.is_empty() {
        bail!("Admin password cannot be empty.");
    }
    let confirm = rpassword::prompt_password("Confirm admin password: ")?;
    if password != confirm {
        bail!("Passwords did not match.");
    }
    Ok(password)
}

fn list_banned_ips(show_all: bool) -> Result<()> {
    let settings = Settings::load()?;
    init_db(&settings.db_path())?;
    with_conn(&settings.db_path(), |conn| {
        purge_old_login_attempts(conn)?;
        let now = now_iso();
        let sql = if show_all {
            "SELECT id, ip_address, failed_count, last_attempt_at, banned_until FROM login_attempts ORDER BY banned_until DESC, last_attempt_at DESC"
        } else {
            "SELECT id, ip_address, failed_count, last_attempt_at, banned_until FROM login_attempts WHERE banned_until IS NOT NULL AND banned_until > ? ORDER BY banned_until DESC, last_attempt_at DESC"
        };
        let mut stmt = conn.prepare(sql)?;
        let records = if show_all {
            stmt.query_map([], ban_row)?
                .collect::<std::result::Result<Vec<_>, _>>()?
        } else {
            stmt.query_map([now], ban_row)?
                .collect::<std::result::Result<Vec<_>, _>>()?
        };
        if records.is_empty() {
            println!(
                "{}",
                if show_all {
                    "No login attempt records found."
                } else {
                    "No banned IP addresses found."
                }
            );
            return Ok(());
        }
        println!(
            "{:<6} {:<45} {:<8} {:<32} Banned Until",
            "ID", "IP Address", "Failures", "Last Attempt"
        );
        for (id, ip, failures, last, banned) in records {
            println!(
                "{id:<6} {ip:<45} {failures:<8} {last:<32} {}",
                banned.unwrap_or_else(|| "-".into())
            );
        }
        Ok(())
    })
}

fn ban_row(
    row: &rusqlite::Row<'_>,
) -> rusqlite::Result<(i64, String, i64, String, Option<String>)> {
    Ok((
        row.get(0)?,
        row.get(1)?,
        row.get(2)?,
        row.get(3)?,
        row.get(4)?,
    ))
}

fn unban_ip(id: i64) -> Result<()> {
    let settings = Settings::load()?;
    init_db(&settings.db_path())?;
    with_conn(&settings.db_path(), |conn| {
        let ip: Option<String> = conn
            .query_row(
                "SELECT ip_address FROM login_attempts WHERE id = ?",
                [id],
                |r| r.get(0),
            )
            .optional()?;
        let Some(ip) = ip else {
            bail!("No login attempt record found with ID {id}.");
        };
        conn.execute("DELETE FROM login_attempts WHERE id = ?", [id])?;
        println!("Removed login attempt record {id} for {ip}.");
        Ok(())
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use image::{DynamicImage, ImageFormat, Rgb, RgbImage};
    use std::io::Cursor;

    #[test]
    fn upload_tokens_are_url_safe_and_long_enough() {
        assert!(validate_upload_token("client-2026_A").is_ok());
        assert!(validate_upload_token("short").is_err());
        assert!(validate_upload_token("not/a/path").is_err());
        assert!(validate_upload_token("contains spaces").is_err());
    }

    #[test]
    fn admin_upload_token_persists_in_database() -> Result<()> {
        let data_dir = env::temp_dir().join(format!("receipt-upload-test-{}", Uuid::new_v4()));
        let settings = Settings {
            admin_username: "admin".into(),
            admin_password: "password".into(),
            secret_key: "test-secret".into(),
            upload_token: "configured-token".into(),
            app_base_url: "http://localhost:8725".into(),
            upload_dir: data_dir.join("receipts"),
            data_dir: data_dir.clone(),
            max_upload_bytes: 1024,
        };
        init_db(&settings.db_path())?;
        assert_eq!(load_upload_token(&settings)?, "configured-token");

        save_upload_token(&settings, "admin-selected-token")?;
        assert_eq!(load_upload_token(&settings)?, "admin-selected-token");

        fs::remove_dir_all(data_dir)?;
        Ok(())
    }

    #[test]
    fn uploaded_images_are_resized_and_written_as_pdf_pages() -> Result<()> {
        let source = DynamicImage::ImageRgb8(RgbImage::from_pixel(
            MAX_IMAGE_DIMENSION + 400,
            900,
            Rgb([240, 240, 240]),
        ));
        let mut encoded = Cursor::new(Vec::new());
        source.write_to(&mut encoded, ImageFormat::Png)?;

        let image = prepare_pdf_image(encoded.get_ref())?;
        assert_eq!(image.width, MAX_IMAGE_DIMENSION);
        assert!(image.height < 900);
        assert!(image.jpeg.starts_with(&[0xff, 0xd8]));

        let output = env::temp_dir().join(format!("receipt-upload-test-{}.pdf", Uuid::new_v4()));
        write_pdf(&[image], &output)?;
        let pdf = fs::read(&output)?;
        fs::remove_file(output)?;

        assert!(pdf.starts_with(b"%PDF-1.4"));
        assert!(pdf.ends_with(b"%%EOF\n"));
        assert_eq!(
            pdf.windows(b"/Type /Page ".len())
                .filter(|window| *window == b"/Type /Page ")
                .count(),
            1
        );
        Ok(())
    }
}
