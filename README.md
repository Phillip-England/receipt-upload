# receipt-upload

`receipt-upload` is a small receipt collection portal for one admin user.

The admin logs in, manages cardholders and stores, and reviews uploaded receipts. Cardholders do not have accounts. They receive a secret upload link, choose their name, enter receipt details, upload one or more receipt images, and the app converts those files into one PDF per expense.

## Features

- Single admin login configured by environment variables.
- CLI commands for setting admin username, password, secret key, upload token, base URL, and data directory.
- Secret cardholder upload URL: `/upload/{UPLOAD_TOKEN}`.
- Cardholder management from the admin portal.
- Store dropdown management from the admin portal.
- Multiple receipt images per upload, merged into one PDF.
- Uploaded PDFs stored on disk.
- Receipt metadata stored in SQLite.
- Admin receipt list with download, archive, and delete actions.
- App disk usage shown on the admin dashboard.
- Failed admin login protection with IP bans.
- CLI commands to list and remove banned IPs.
- Dockerfile for VPS deployment.

## Requirements

- Python 3.11 or newer.
- `uv` for dependency management.
- ImageMagick for receipt image to PDF conversion.

The Docker image installs ImageMagick automatically. Outside Docker, the app checks for ImageMagick on startup and makes a best-effort attempt to install it when `AUTO_INSTALL_IMAGEMAGICK=true`.

## Install Locally

From the project directory:

```bash
uv sync
```

Run the app without installing the CLI:

```bash
uv run receipt-upload
```

The default app port is `8725`:

```text
http://localhost:8725/admin/login
```

## Install The CLI

Install the `receipt-upload` command from this checkout:

```bash
make install
```

Confirm it is available:

```bash
receipt-upload --help
```

Run the app:

```bash
receipt-upload
```

Or explicitly:

```bash
receipt-upload serve --host 0.0.0.0 --port 8725
```

`receipt-upload --host 0.0.0.0 --port 8725` also works as shorthand for `receipt-upload serve`.

## First-Time Setup

The app reads configuration from environment variables. If a `.env` file exists in the current directory, it is loaded automatically.

Create `.env` with all supported variables set to sensible defaults:

```bash
receipt-upload init-env
```

This writes missing values without replacing values already present in `.env`. To reset every supported key to the built-in default:

```bash
receipt-upload init-env --overwrite
```

The default admin login is `admin` / `password`. The web UI shows a warning while either default credential is still active. Change the admin credentials manually with the CLI:

```bash
receipt-upload set-username admin
receipt-upload set-password
```

Before production use, also replace the secret key, upload token, and public base URL:

```bash
receipt-upload generate-secret-key
receipt-upload set-upload-token your-secret-upload-token
receipt-upload set-base-url http://localhost:8725
```

To set the password without an interactive prompt:

```bash
receipt-upload set-password "replace-this-password"
```

To write a different env file:

```bash
receipt-upload --env-file /path/to/.env set-username admin
```

## CLI Reference

Run the server:

```bash
receipt-upload
receipt-upload serve
receipt-upload serve --host 0.0.0.0 --port 8725
receipt-upload serve --reload
```

Set configuration:

```bash
receipt-upload init-env
receipt-upload init-env --overwrite
receipt-upload set-username admin
receipt-upload set-password
receipt-upload set-password "new-password"
receipt-upload generate-secret-key
receipt-upload set-upload-token "secret-upload-token"
receipt-upload set-base-url "https://receipts.example.com"
receipt-upload set-data-dir "/var/lib/receipt-upload"
```

Manage login bans:

```bash
receipt-upload list-banned-ips
receipt-upload list-banned-ips --all
receipt-upload unban-ip 1
```

`list-banned-ips` shows active bans. `list-banned-ips --all` shows all login attempt records, including IPs with failed attempts that are not currently banned.

## Environment Variables

| Variable | Default | Description |
| --- | --- | --- |
| `ADMIN_USERNAME` | `admin` | Admin login username. |
| `ADMIN_PASSWORD` | `password` | Admin login password. Change this before use. |
| `SECRET_KEY` | `dev-secret-change-me` | Signs the admin session cookie. Use `generate-secret-key`. |
| `UPLOAD_TOKEN` | `dev-upload-token` | Secret token used in `/upload/{UPLOAD_TOKEN}`. |
| `APP_BASE_URL` | `http://localhost:8725` | Public URL shown in the admin portal for the upload link. |
| `DATA_DIR` | `./data` | Directory for SQLite and uploaded receipt PDFs. |
| `MAX_UPLOAD_MB` | `50` | Maximum total upload size per receipt submission. |
| `AUTO_INSTALL_IMAGEMAGICK` | `true` | Best-effort ImageMagick install when missing. |

Example `.env`:

```env
ADMIN_USERNAME=admin
ADMIN_PASSWORD=password
SECRET_KEY=replace-with-a-long-random-secret
UPLOAD_TOKEN=replace-with-a-secret-token
APP_BASE_URL=https://receipts.example.com
DATA_DIR=/var/lib/receipt-upload
MAX_UPLOAD_MB=50
AUTO_INSTALL_IMAGEMAGICK=true
```

## Admin Usage

1. Open `http://localhost:8725/admin/login`.
2. Log in with `ADMIN_USERNAME` and `ADMIN_PASSWORD`.
3. Add cardholders.
4. Add stores.
5. Copy the secret upload link from the admin dashboard.
6. Send that link to cardholders.
7. Review uploads from the admin dashboard.
8. Download, archive, or delete uploaded receipts as needed.

The admin dashboard shows how much disk space the app is using under `DATA_DIR`.

## Cardholder Usage

Cardholders open the secret upload link and submit:

- Their name.
- Receipt total.
- Purchase location.
- Store to charge.
- Optional note.
- One or more receipt files.

If multiple images are uploaded for one expense, the app merges them into one PDF.

## Receipt Storage

The app stores data under `DATA_DIR`.

Default local layout:

```text
data/
  app.sqlite3
  receipts/
    <generated-id>.pdf
```

SQLite stores metadata such as cardholder name, total, purchase location, selected store, note, timestamps, archive status, and PDF path.

PDFs are saved directly on disk.

## Login Ban System

The admin login form tracks failed attempts by IP address.

- 3 failed attempts bans the IP.
- Bans last 24 hours.
- Old login attempt records are purged automatically during login handling.
- The table uses an integer `id` for each IP record so bans can be removed easily.

List active bans:

```bash
receipt-upload list-banned-ips
```

Example output:

```text
ID     IP Address                                    Failures Last Attempt                     Banned Until
2      203.0.113.88                                  3        2026-06-03T19:11:42+00:00       2026-06-04T19:11:42+00:00
```

Remove a ban:

```bash
receipt-upload unban-ip 2
```

## Docker

Build:

```bash
docker build -t receipt-upload .
```

Run:

```bash
docker run --rm -p 8725:8725 \
  -e ADMIN_USERNAME=admin \
  -e ADMIN_PASSWORD='replace-me' \
  -e SECRET_KEY='replace-with-a-long-random-secret' \
  -e UPLOAD_TOKEN='replace-with-a-secret-token' \
  -e APP_BASE_URL='https://receipts.example.com' \
  -v receipt-upload-data:/app/data \
  receipt-upload
```

ImageMagick is installed in the container. Uploaded PDFs and SQLite data are stored under `/app/data`.

## VPS Deployment Notes

Recommended production shape:

- Run the app on `127.0.0.1:8725` or `0.0.0.0:8725`.
- Put Nginx, Caddy, or another reverse proxy in front of it.
- Use HTTPS.
- Set `APP_BASE_URL` to the public HTTPS URL.
- Persist `DATA_DIR`.
- Back up `DATA_DIR`.

Example direct server run:

```bash
receipt-upload set-username admin
receipt-upload set-password
receipt-upload generate-secret-key
receipt-upload set-upload-token "long-random-upload-token"
receipt-upload set-base-url "https://receipts.example.com"
receipt-upload set-data-dir "/var/lib/receipt-upload"
receipt-upload serve --host 127.0.0.1 --port 8725
```

If deployed behind a reverse proxy, make sure it forwards the real client IP using `X-Forwarded-For`, because login bans use the request IP address.

## Make Targets

```bash
make install
make sync
make run
make clean
```

- `make install`: installs the CLI with `uv tool install --force .`.
- `make sync`: installs project dependencies into `.venv`.
- `make run`: runs the app on port `8725`.
- `make clean`: removes local build artifacts and `.venv`.

## Troubleshooting

If the CLI is not found after `make install`, confirm `uv`'s tool bin directory is on your `PATH`:

```bash
which receipt-upload
```

If you lock yourself out while testing login failures:

```bash
receipt-upload list-banned-ips
receipt-upload unban-ip <id>
```

If ImageMagick is missing, install it manually or run the Docker image. On macOS:

```bash
brew install imagemagick
```

On Debian or Ubuntu:

```bash
sudo apt-get update
sudo apt-get install -y imagemagick
```

If the app starts with default credentials, your `.env` file may not be in the directory where you started `receipt-upload`, or the environment variables may not be exported.

## Security Notes

- Change `ADMIN_USERNAME`, `ADMIN_PASSWORD`, `SECRET_KEY`, and `UPLOAD_TOKEN` before use.
- Treat the upload URL as secret.
- Use HTTPS in production.
- Back up `DATA_DIR`.
- Delete old uploads when disk usage grows.
