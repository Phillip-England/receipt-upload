from __future__ import annotations

import argparse
import getpass
import secrets
import string
from pathlib import Path

from receipt_upload.config import DEFAULT_ENV


ENV_KEYS = {
    "username": "ADMIN_USERNAME",
    "password": "ADMIN_PASSWORD",
    "secret-key": "SECRET_KEY",
    "upload-token": "UPLOAD_TOKEN",
    "base-url": "APP_BASE_URL",
    "data-dir": "DATA_DIR",
}

DEFAULT_HOST = "0.0.0.0"
DEFAULT_PORT = 8725


def main() -> None:
    parser = argparse.ArgumentParser(description="Manage and run the receipt-upload application.")
    parser.add_argument("--env-file", default=".env", help="Environment file to update. Defaults to .env.")
    subparsers = parser.add_subparsers(dest="command")

    serve_parser = subparsers.add_parser("serve", help="Run the web application.")
    serve_parser.add_argument("--host", default=DEFAULT_HOST, help=f"Host interface to bind. Defaults to {DEFAULT_HOST}.")
    serve_parser.add_argument("--port", default=DEFAULT_PORT, type=int, help=f"Port to bind. Defaults to {DEFAULT_PORT}.")
    serve_parser.add_argument("--reload", action="store_true", help="Enable uvicorn reload mode for development.")
    parser.set_defaults(host=DEFAULT_HOST, port=DEFAULT_PORT, reload=False)

    init_parser = subparsers.add_parser("init-env", help="Write sensible default environment values to .env.")
    init_parser.add_argument(
        "--overwrite",
        action="store_true",
        help="Replace existing environment values in the target env file.",
    )

    _add_value_command(subparsers, "set-username", "ADMIN_USERNAME", "Admin username")
    _add_value_command(subparsers, "set-password", "ADMIN_PASSWORD", "Admin password")
    _add_value_command(subparsers, "set-upload-token", "UPLOAD_TOKEN", "Secret receipt upload URL token")
    _add_value_command(subparsers, "set-base-url", "APP_BASE_URL", "Public base URL")
    _add_value_command(subparsers, "set-data-dir", "DATA_DIR", "Application data directory")

    secret_parser = subparsers.add_parser("generate-secret-key", help="Generate and save a random SECRET_KEY.")
    secret_parser.add_argument("--length", type=int, default=48, help="Generated secret length. Defaults to 48.")

    banned_parser = subparsers.add_parser("list-banned-ips", help="List currently banned login IP addresses.")
    banned_parser.add_argument("--all", action="store_true", help="Show all login attempt records, not only active bans.")

    unban_parser = subparsers.add_parser("unban-ip", help="Remove a login attempt or ban record by ID.")
    unban_parser.add_argument("id", type=int, help="Login attempt ID from list-banned-ips.")

    # Keep the original `receipt-upload --host ...` behavior as shorthand for `serve`.
    args = parser.parse_args(_normalize_serve_args())
    env_path = Path(args.env_file)

    if args.command in {None, "serve"}:
        _serve(args)
        return
    if args.command == "init-env":
        written, preserved = _init_env(env_path, args.overwrite)
        print(f"Updated {env_path} with default environment values.")
        if written:
            print("Set: " + ", ".join(written))
        if preserved:
            print("Preserved existing values: " + ", ".join(preserved))
        print('Default admin login is "admin" / "password"; change it with set-username and set-password.')
        return
    if args.command == "set-password":
        value = args.value if args.value else _prompt_password()
        _set_env(env_path, "ADMIN_PASSWORD", value)
        print(f"Updated ADMIN_PASSWORD in {env_path}")
        return
    if args.command == "generate-secret-key":
        alphabet = string.ascii_letters + string.digits + "-_"
        value = "".join(secrets.choice(alphabet) for _ in range(args.length))
        _set_env(env_path, "SECRET_KEY", value)
        print(f"Updated SECRET_KEY in {env_path}")
        return
    if args.command == "list-banned-ips":
        _list_banned_ips(args.all)
        return
    if args.command == "unban-ip":
        _unban_ip(args.id)
        return
    for command_name, env_key in ENV_KEYS.items():
        if args.command == f"set-{command_name}":
            _set_env(env_path, env_key, args.value)
            print(f"Updated {env_key} in {env_path}")
            return


def _normalize_serve_args() -> list[str] | None:
    import sys

    if len(sys.argv) > 1 and sys.argv[1].startswith("--") and sys.argv[1] not in {"--env-file", "--help"}:
        return ["serve", *sys.argv[1:]]
    return None


def _add_value_command(
    subparsers: argparse._SubParsersAction[argparse.ArgumentParser],
    name: str,
    env_key: str,
    help_text: str,
) -> None:
    command = subparsers.add_parser(name, help=f"Set {env_key} in .env.")
    required = name != "set-password"
    command.add_argument("value", nargs=None if required else "?", help=help_text)


def _serve(args: argparse.Namespace) -> None:
    import uvicorn

    uvicorn.run("receipt_upload.main:app", host=args.host, port=args.port, reload=args.reload)


def _list_banned_ips(show_all: bool) -> None:
    from receipt_upload.config import load_settings
    from receipt_upload.db import connect, init_db
    from receipt_upload.security import iso, purge_old_login_attempts, utc_now

    settings = load_settings()
    db_path = settings.data_dir / "app.sqlite3"
    init_db(db_path)
    with connect(db_path) as conn:
        purge_old_login_attempts(conn)
        where = "" if show_all else "WHERE banned_until IS NOT NULL AND banned_until > ?"
        params = () if show_all else (iso(utc_now()),)
        records = conn.execute(
            f"""
            SELECT id, ip_address, failed_count, last_attempt_at, banned_until
            FROM login_attempts
            {where}
            ORDER BY banned_until DESC, last_attempt_at DESC
            """,
            params,
        ).fetchall()
    if not records:
        print("No banned IP addresses found." if not show_all else "No login attempt records found.")
        return
    print(f"{'ID':<6} {'IP Address':<45} {'Failures':<8} {'Last Attempt':<32} Banned Until")
    for record in records:
        print(
            f"{record['id']:<6} {record['ip_address']:<45} {record['failed_count']:<8} "
            f"{record['last_attempt_at']:<32} {record['banned_until'] or '-'}"
        )


def _unban_ip(login_attempt_id: int) -> None:
    from receipt_upload.config import load_settings
    from receipt_upload.db import connect, init_db

    settings = load_settings()
    db_path = settings.data_dir / "app.sqlite3"
    init_db(db_path)
    with connect(db_path) as conn:
        record = conn.execute(
            "SELECT ip_address FROM login_attempts WHERE id = ?",
            (login_attempt_id,),
        ).fetchone()
        if not record:
            raise SystemExit(f"No login attempt record found with ID {login_attempt_id}.")
        conn.execute("DELETE FROM login_attempts WHERE id = ?", (login_attempt_id,))
        print(f"Removed login attempt record {login_attempt_id} for {record['ip_address']}.")


def _prompt_password() -> str:
    password = getpass.getpass("Admin password: ")
    confirmation = getpass.getpass("Confirm admin password: ")
    if password != confirmation:
        raise SystemExit("Passwords did not match.")
    if not password:
        raise SystemExit("Password cannot be empty.")
    return password


def _set_env(path: Path, key: str, value: str) -> None:
    if not value:
        raise SystemExit(f"{key} cannot be empty.")
    path.parent.mkdir(parents=True, exist_ok=True)
    lines = path.read_text().splitlines() if path.exists() else []
    rendered = f"{key}={_quote_env_value(value)}"
    updated = False
    output: list[str] = []
    for line in lines:
        stripped = line.strip()
        if stripped and not stripped.startswith("#") and stripped.split("=", 1)[0].strip() == key:
            output.append(rendered)
            updated = True
        else:
            output.append(line)
    if not updated:
        output.append(rendered)
    path.write_text("\n".join(output) + "\n")


def _init_env(path: Path, overwrite: bool) -> tuple[list[str], list[str]]:
    path.parent.mkdir(parents=True, exist_ok=True)
    lines = path.read_text().splitlines() if path.exists() else []
    existing_keys = {
        line.split("=", 1)[0].strip()
        for line in lines
        if line.strip() and not line.strip().startswith("#") and "=" in line
    }
    output: list[str] = []
    written: list[str] = []
    preserved: list[str] = []
    for line in lines:
        stripped = line.strip()
        if not stripped or stripped.startswith("#") or "=" not in line:
            output.append(line)
            continue
        key = line.split("=", 1)[0].strip()
        if key in DEFAULT_ENV and overwrite:
            output.append(f"{key}={_quote_env_value(DEFAULT_ENV[key])}")
            written.append(key)
        else:
            output.append(line)
            if key in DEFAULT_ENV:
                preserved.append(key)
    for key, value in DEFAULT_ENV.items():
        if key not in existing_keys:
            output.append(f"{key}={_quote_env_value(value)}")
            written.append(key)
    path.write_text("\n".join(output) + "\n")
    return written, preserved


def _quote_env_value(value: str) -> str:
    if not value or any(char.isspace() or char in "\"'#$" for char in value):
        escaped = value.replace("\\", "\\\\").replace('"', '\\"')
        return f'"{escaped}"'
    return value
