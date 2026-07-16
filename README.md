# receipt-upload

`receipt-upload` is a small Rust receipt collection portal for one admin user.

The admin logs in, manages cardholders and stores, and reviews uploaded receipts. Cardholders do not have accounts. They receive a secret upload link, choose their name, enter receipt details, upload one or more receipt images, and the app creates one compressed PDF per expense.

## Features

- Axum-based Rust web app.
- Single admin login configured by an explicit environment-style config file.
- Admin-managed secret cardholder upload URL: `/upload/{UPLOAD_TOKEN}`.
- Cardholder and store management from the admin portal.
- Multiple store checkboxes per receipt.
- Multiple receipt images per upload, resized and merged into one PDF.
- Client-side image shrinking before upload when the browser supports it.
- Server-side image resizing and JPEG compression before PDF generation.
- Uploaded PDFs stored on disk.
- Receipt metadata stored in SQLite.
- Admin receipt list with download, archive, and delete actions.
- App disk usage shown on the admin dashboard.
- Failed admin login protection with IP bans.
- CLI commands to list and remove banned IPs.
- Dockerfile for VPS deployment.

## Requirements

- Rust stable toolchain.
- A C compiler for bundled SQLite builds.

No external command or image conversion package is required. Cargo compiles image decoding, resizing, PDF generation, and bundled SQLite support into `receipt-upload` itself.

## Install

From the project directory, one command installs the app and all of its Rust dependencies:

```bash
make install
```

This is equivalent to:

```bash
cargo install --locked --path . --force
```

No separate PDF dependency setup is needed. Initialize and start the installed app with:

```bash
receipt-upload init
# Edit ADMIN_PASSWORD in ./config/.env, then:
receipt-upload
```

The default admin URL is `http://localhost:8725/admin/login`. To select another bind address or port:

```bash
receipt-upload serve --config ./runtime/app.env --host 0.0.0.0 --port 8725
```

`receipt-upload --config ./runtime/app.env --host 0.0.0.0 --port 8725` also works as shorthand for `receipt-upload serve`.

## First-Time Setup

Initialize the configuration, SQLite database, and receipt storage:

```bash
receipt-upload init
```

This creates `./config/.env`, `./data/app.sqlite3`, and `./data/receipts/`.

Edit `ADMIN_PASSWORD` before real use, then validate the file without starting the server:

```bash
receipt-upload config check
```

Start the app only after validation succeeds:

```bash
receipt-upload serve
```

The defaults are `./config/.env` and `./data`. Custom locations can be initialized with:

```bash
receipt-upload init --config ./runtime/app.env --data-dir /var/lib/receipt-upload
```

To initialize only one part, use `receipt-upload config init` or
`receipt-upload database init`.

Existing config files can be edited with the CLI:

```bash
receipt-upload set-username --config ./runtime/app.env admin
receipt-upload set-password --config ./runtime/app.env
receipt-upload set-config --config ./runtime/app.env APP_BASE_URL https://receipts.example.com
receipt-upload set-config --config ./runtime/app.env DATA_DIR /var/lib/receipt-upload
```

## CLI Reference

```bash
receipt-upload init
receipt-upload init --force
receipt-upload init --config ./runtime/app.env --data-dir /var/lib/receipt-upload
receipt-upload database init
receipt-upload --config ./runtime/app.env
receipt-upload serve --config ./runtime/app.env
receipt-upload serve --config ./runtime/app.env --host 0.0.0.0 --port 8725
receipt-upload config init --path ./runtime/app.env
receipt-upload config init --path ./runtime/app.env --force
receipt-upload config check --config ./runtime/app.env
receipt-upload set-username --config ./runtime/app.env admin
receipt-upload set-password --config ./runtime/app.env
receipt-upload set-password --config ./runtime/app.env 'replace-this-password'
receipt-upload set-config --config ./runtime/app.env APP_BASE_URL https://receipts.example.com
receipt-upload set-config --config ./runtime/app.env DATA_DIR /var/lib/receipt-upload
receipt-upload generate-secret-key
receipt-upload generate-secret-key --raw
receipt-upload generate-upload-token
receipt-upload generate-upload-token --raw
receipt-upload list-banned-ips --config ./runtime/app.env
receipt-upload list-banned-ips --config ./runtime/app.env --all
receipt-upload unban-ip --config ./runtime/app.env 1
```

## Configuration File

`receipt-upload` reads one environment-style config file selected by `--config`. It does not search the working directory for `.env`, and it does not merge runtime environment variables into application settings.

The committed `app.env.example` documents every supported setting without production secrets. `receipt-upload config init --path ./runtime/app.env` creates a local config file with restrictive permissions on supported operating systems. Generated runtime config files under `runtime/` and `config/` are ignored by Git.

| Variable | Example | Description |
| --- | --- | --- |
| `ADMIN_USERNAME` | `admin` | Admin login username. |
| `ADMIN_PASSWORD` | `REPLACE_ME` | Admin login password. Change this before use. |
| `SECRET_KEY` | generated by init | Signs the admin session cookie. |
| `UPLOAD_TOKEN` | generated by init | Secret token used in `/upload/{UPLOAD_TOKEN}`. |
| `APP_BASE_URL` | `http://localhost:8725` | Public URL shown in the admin portal for the upload link. |
| `DATA_DIR` | `./data` | Directory for SQLite and uploaded receipt PDFs. |
| `MAX_UPLOAD_MB` | `50` | Maximum total upload size per receipt submission. |

Configuration errors identify the key without printing sensitive values, for example:

```text
configuration error: ADMIN_PASSWORD is required
```

## Admin Usage

1. Open `http://localhost:8725/admin/login`.
2. Log in with `ADMIN_USERNAME` and `ADMIN_PASSWORD`.
3. Add cardholders.
4. Add stores.
5. Set the public hostname and secret code, then copy the resulting upload link from the admin dashboard.
6. Send that link to cardholders.
7. Review uploads from the admin dashboard.
8. Download, archive, or delete uploaded receipts as needed.

The admin dashboard shows how much disk space the app is using under `DATA_DIR`.
Changing the secret code disables the previous upload link immediately. The admin-selected public URL and code are stored in the application database and take priority over `APP_BASE_URL` and `UPLOAD_TOKEN` on later starts.

## Cardholder Usage

Cardholders open the secret upload link and submit:

- Their name.
- Receipt total.
- Place of purchase.
- One or more stores.
- Optional description.
- Optional notes.
- One or more receipt images.

Each submission becomes one final PDF.

## Receipt Storage

The app stores data under `DATA_DIR`.

```text
data/
  app.sqlite3
  receipts/
    <generated-id>.pdf
```

SQLite stores receipt metadata, selected stores, timestamps, archive status, and PDF paths.

## Login Ban System

- 3 failed attempts bans the IP.
- Bans last 24 hours.
- Old login attempt records are purged automatically during login handling.
- The table uses an integer `id` for each IP record so bans can be removed easily.

## Docker

Build:

```bash
docker build -t receipt-upload .
```

Run:

```bash
docker run --rm -p 8725:8725 \
  -v "$PWD/config/.env:/app/config/.env:ro" \
  -v receipt-upload-data:/app/data \
  receipt-upload
```

Uploaded PDFs and SQLite data are stored under the configured `DATA_DIR`. For the
Docker command above, set `DATA_DIR=/app/data` in `config/.env`.

## Make Targets

```bash
make install
make sync
make run
make check
make clean
```
