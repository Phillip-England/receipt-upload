from __future__ import annotations

import base64
import hashlib
import hmac
import json
import sqlite3
from datetime import datetime, timedelta, timezone
from typing import Any

from fastapi import Request


SESSION_COOKIE = "receipt_upload_session"
BAN_HOURS = 24
MAX_FAILED_LOGINS = 3


def utc_now() -> datetime:
    return datetime.now(timezone.utc)


def iso(dt: datetime) -> str:
    return dt.isoformat()


def parse_iso(value: str | None) -> datetime | None:
    if not value:
        return None
    return datetime.fromisoformat(value)


def client_ip(request: Request) -> str:
    forwarded = request.headers.get("x-forwarded-for")
    if forwarded:
        return forwarded.split(",", 1)[0].strip()
    if request.client:
        return request.client.host
    return "unknown"


def sign_session(payload: dict[str, Any], secret_key: str) -> str:
    body = base64.urlsafe_b64encode(json.dumps(payload, separators=(",", ":")).encode()).decode()
    signature = hmac.new(secret_key.encode(), body.encode(), hashlib.sha256).hexdigest()
    return f"{body}.{signature}"


def read_session(cookie_value: str | None, secret_key: str) -> dict[str, Any] | None:
    if not cookie_value or "." not in cookie_value:
        return None
    body, signature = cookie_value.rsplit(".", 1)
    expected = hmac.new(secret_key.encode(), body.encode(), hashlib.sha256).hexdigest()
    if not hmac.compare_digest(signature, expected):
        return None
    try:
        return json.loads(base64.urlsafe_b64decode(body.encode()).decode())
    except (ValueError, json.JSONDecodeError):
        return None


def purge_old_login_attempts(conn: sqlite3.Connection) -> None:
    cutoff = iso(utc_now() - timedelta(hours=BAN_HOURS))
    conn.execute(
        "DELETE FROM login_attempts WHERE last_attempt_at < ? AND (banned_until IS NULL OR banned_until < ?)",
        (cutoff, iso(utc_now())),
    )


def is_ip_banned(conn: sqlite3.Connection, ip_address: str) -> bool:
    record = conn.execute(
        "SELECT banned_until FROM login_attempts WHERE ip_address = ?",
        (ip_address,),
    ).fetchone()
    banned_until = parse_iso(record["banned_until"]) if record else None
    return bool(banned_until and banned_until > utc_now())


def record_failed_login(conn: sqlite3.Connection, ip_address: str) -> None:
    now = utc_now()
    record = conn.execute(
        "SELECT failed_count FROM login_attempts WHERE ip_address = ?",
        (ip_address,),
    ).fetchone()
    failed_count = int(record["failed_count"]) + 1 if record else 1
    banned_until = iso(now + timedelta(hours=BAN_HOURS)) if failed_count >= MAX_FAILED_LOGINS else None
    conn.execute(
        """
        INSERT INTO login_attempts (ip_address, failed_count, last_attempt_at, banned_until)
        VALUES (?, ?, ?, ?)
        ON CONFLICT(ip_address) DO UPDATE SET
            failed_count = excluded.failed_count,
            last_attempt_at = excluded.last_attempt_at,
            banned_until = excluded.banned_until
        """,
        (ip_address, failed_count, iso(now), banned_until),
    )


def clear_login_attempts(conn: sqlite3.Connection, ip_address: str) -> None:
    conn.execute("DELETE FROM login_attempts WHERE ip_address = ?", (ip_address,))
