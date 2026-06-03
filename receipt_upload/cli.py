from __future__ import annotations

import argparse
import getpass
import secrets
import shlex
import string


DEFAULT_HOST = "0.0.0.0"
DEFAULT_PORT = 8725
ENV_HELP = """Environment variables:
  ADMIN_USERNAME              Admin login username (default: admin)
  ADMIN_PASSWORD              Admin login password (default: password)
  SECRET_KEY                  Session signing key
  UPLOAD_TOKEN                Secret token used in /upload/{UPLOAD_TOKEN}
  APP_BASE_URL                Public base URL (default: http://localhost:8725)
  DATA_DIR                    SQLite/PDF storage directory (default: ./data)
  MAX_UPLOAD_MB               Max total upload size (default: 50)
  AUTO_INSTALL_IMAGEMAGICK    Best-effort ImageMagick install (default: true)
  RECEIPT_UPLOAD_CONFIG       Persistent defaults file path

Example:
  receipt-upload set-username admin
  receipt-upload set-password
  receipt-upload set-config APP_BASE_URL https://receipts.example.com
  export ADMIN_USERNAME=admin
  export ADMIN_PASSWORD='replace-me'
  export SECRET_KEY='replace-with-a-long-random-secret'
  export UPLOAD_TOKEN='replace-with-a-secret-token'
  export APP_BASE_URL='https://receipts.example.com'
  export DATA_DIR='/var/lib/receipt-upload'
  receipt-upload serve
"""


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Run the receipt-upload application using process environment variables.",
        epilog=ENV_HELP,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    subparsers = parser.add_subparsers(dest="command")

    serve_parser = subparsers.add_parser("serve", help="Run the web application.")
    serve_parser.add_argument("--host", default=DEFAULT_HOST, help=f"Host interface to bind. Defaults to {DEFAULT_HOST}.")
    serve_parser.add_argument("--port", default=DEFAULT_PORT, type=int, help=f"Port to bind. Defaults to {DEFAULT_PORT}.")
    serve_parser.add_argument("--reload", action="store_true", help="Enable uvicorn reload mode for development.")
    parser.set_defaults(host=DEFAULT_HOST, port=DEFAULT_PORT, reload=False)

    secret_parser = subparsers.add_parser("generate-secret-key", help="Print a shell export for a random SECRET_KEY.")
    secret_parser.add_argument("--length", type=int, default=48, help="Generated secret length. Defaults to 48.")
    secret_parser.add_argument("--raw", action="store_true", help="Print only the generated value.")

    token_parser = subparsers.add_parser("generate-upload-token", help="Print a shell export for a random UPLOAD_TOKEN.")
    token_parser.add_argument("--length", type=int, default=32, help="Generated token length. Defaults to 32.")
    token_parser.add_argument("--raw", action="store_true", help="Print only the generated value.")

    username_parser = subparsers.add_parser("set-username", help="Persist a default admin username.")
    username_parser.add_argument("username", help="Admin username to use when ADMIN_USERNAME is not set.")

    password_parser = subparsers.add_parser("set-password", help="Persist a default admin password.")
    password_parser.add_argument(
        "password",
        nargs="?",
        help="Admin password to use when ADMIN_PASSWORD is not set. Omit to enter it securely.",
    )

    config_parser = subparsers.add_parser("set-config", help="Persist a default configuration value.")
    config_parser.add_argument("name", choices=_config_keys(), help="Environment variable name to persist.")
    config_parser.add_argument("value", help="Value to use when the environment variable is not set.")

    banned_parser = subparsers.add_parser("list-banned-ips", help="List currently banned login IP addresses.")
    banned_parser.add_argument("--all", action="store_true", help="Show all login attempt records, not only active bans.")

    unban_parser = subparsers.add_parser("unban-ip", help="Remove a login attempt or ban record by ID.")
    unban_parser.add_argument("id", type=int, help="Login attempt ID from list-banned-ips.")

    # Keep the original `receipt-upload --host ...` behavior as shorthand for `serve`.
    args = parser.parse_args(_normalize_serve_args())

    if args.command in {None, "serve"}:
        _serve(args)
        return
    if args.command == "generate-secret-key":
        value = _generate_urlsafe_value(args.length)
        _print_generated_env("SECRET_KEY", value, args.raw)
        return
    if args.command == "generate-upload-token":
        value = _generate_urlsafe_value(args.length)
        _print_generated_env("UPLOAD_TOKEN", value, args.raw)
        return
    if args.command == "set-username":
        _set_config_value("ADMIN_USERNAME", args.username, "admin username")
        return
    if args.command == "set-password":
        password = args.password if args.password is not None else _prompt_password()
        _set_config_value("ADMIN_PASSWORD", password, "admin password")
        return
    if args.command == "set-config":
        _set_config_value(args.name, args.value, args.name)
        return
    if args.command == "list-banned-ips":
        _list_banned_ips(args.all)
        return
    if args.command == "unban-ip":
        _unban_ip(args.id)
        return


def _normalize_serve_args() -> list[str] | None:
    import sys

    if len(sys.argv) > 1 and sys.argv[1].startswith("--") and sys.argv[1] != "--help":
        return ["serve", *sys.argv[1:]]
    return None


def _serve(args: argparse.Namespace) -> None:
    import uvicorn

    uvicorn.run("receipt_upload.main:app", host=args.host, port=args.port, reload=args.reload)


def _generate_urlsafe_value(length: int) -> str:
    if length < 1:
        raise SystemExit("Length must be greater than zero.")
    alphabet = string.ascii_letters + string.digits + "-_"
    return "".join(secrets.choice(alphabet) for _ in range(length))


def _print_generated_env(key: str, value: str, raw: bool) -> None:
    if raw:
        print(value)
        return
    print(f"export {key}={shlex.quote(value)}")


def _config_keys() -> list[str]:
    from receipt_upload.config import DEFAULT_ENV

    return sorted(DEFAULT_ENV)


def _set_config_value(key: str, value: str, label: str) -> None:
    if not value:
        raise SystemExit(f"{label.capitalize()} cannot be empty.")
    from receipt_upload.config import save_config_value

    path = save_config_value(key, value)
    print(f"Saved default {label} to {path}.")


def _prompt_password() -> str:
    password = getpass.getpass("Admin password: ")
    if not password:
        raise SystemExit("Admin password cannot be empty.")
    confirm = getpass.getpass("Confirm admin password: ")
    if password != confirm:
        raise SystemExit("Passwords did not match.")
    return password


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
