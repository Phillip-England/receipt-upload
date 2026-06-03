from __future__ import annotations

import sqlite3
from pathlib import Path
from typing import Any


SCHEMA = """
PRAGMA journal_mode = WAL;
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
    store_id INTEGER,
    cardholder_name TEXT NOT NULL,
    store_name TEXT,
    total TEXT NOT NULL,
    purchase_location TEXT NOT NULL,
    note TEXT,
    original_filenames TEXT NOT NULL,
    pdf_path TEXT NOT NULL,
    pdf_size_bytes INTEGER NOT NULL,
    archived_at TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(cardholder_id) REFERENCES cardholders(id) ON DELETE SET NULL,
    FOREIGN KEY(store_id) REFERENCES stores(id) ON DELETE SET NULL
);
CREATE TABLE IF NOT EXISTS login_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ip_address TEXT NOT NULL UNIQUE,
    failed_count INTEGER NOT NULL,
    last_attempt_at TEXT NOT NULL,
    banned_until TEXT
);
"""


def connect(db_path: Path) -> sqlite3.Connection:
    db_path.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(db_path, check_same_thread=False)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA foreign_keys = ON")
    return conn


def init_db(db_path: Path) -> None:
    with connect(db_path) as conn:
        conn.executescript(SCHEMA)
        _migrate_login_attempt_ids(conn)


def _migrate_login_attempt_ids(conn: sqlite3.Connection) -> None:
    columns = conn.execute("PRAGMA table_info(login_attempts)").fetchall()
    if any(column["name"] == "id" for column in columns):
        return
    conn.executescript(
        """
        ALTER TABLE login_attempts RENAME TO login_attempts_old;
        CREATE TABLE login_attempts (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            ip_address TEXT NOT NULL UNIQUE,
            failed_count INTEGER NOT NULL,
            last_attempt_at TEXT NOT NULL,
            banned_until TEXT
        );
        INSERT INTO login_attempts (ip_address, failed_count, last_attempt_at, banned_until)
        SELECT ip_address, failed_count, last_attempt_at, banned_until
        FROM login_attempts_old;
        DROP TABLE login_attempts_old;
        """
    )


def rows(conn: sqlite3.Connection, query: str, params: tuple[Any, ...] = ()) -> list[sqlite3.Row]:
    return conn.execute(query, params).fetchall()


def row(conn: sqlite3.Connection, query: str, params: tuple[Any, ...] = ()) -> sqlite3.Row | None:
    return conn.execute(query, params).fetchone()
