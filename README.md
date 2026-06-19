# receipt-upload

`receipt-upload` is a small Rust receipt collection portal for one admin user.

The admin logs in, manages cardholders and stores, and reviews uploaded receipts. Cardholders do not have accounts. They receive a secret upload link, choose their name, enter receipt details, upload one or more receipt images, and the app creates one compressed PDF per expense.

## Features

- Axum-based Rust web app.
- Single admin login configured by environment variables or saved CLI defaults.
- Secret cardholder upload URL: `/upload/{UPLOAD_TOKEN}`.
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

No separate PDF dependency setup is needed. Start the installed app with:

```bash
receipt-upload
```

The default admin URL is `http://localhost:8725/admin/login`. To select another bind address or port:

```bash
receipt-upload serve --host 0.0.0.0 --port 8725
```

`receipt-upload --host 0.0.0.0 --port 8725` also works as shorthand for `receipt-upload serve`.

## First-Time Setup

Set persistent default admin credentials:

```bash
receipt-upload set-username admin
receipt-upload set-password
```

Other app settings can be persisted the same way:

```bash
receipt-upload set-config APP_BASE_URL https://receipts.example.com
receipt-upload set-config DATA_DIR /var/lib/receipt-upload
```

Runtime environment variables still take priority over saved defaults.

Generate strong secret values:

```bash
receipt-upload generate-secret-key
receipt-upload generate-upload-token
```

## CLI Reference

```bash
receipt-upload
receipt-upload serve
receipt-upload serve --host 0.0.0.0 --port 8725
receipt-upload set-username admin
receipt-upload set-password
receipt-upload set-password 'replace-this-password'
receipt-upload set-config APP_BASE_URL https://receipts.example.com
receipt-upload set-config DATA_DIR /var/lib/receipt-upload
receipt-upload generate-secret-key
receipt-upload generate-secret-key --raw
receipt-upload generate-upload-token
receipt-upload generate-upload-token --raw
receipt-upload list-banned-ips
receipt-upload list-banned-ips --all
receipt-upload unban-ip 1
```

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
| `RECEIPT_UPLOAD_CONFIG` | Platform config path | Persistent defaults file path. |

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
  -e ADMIN_USERNAME=admin \
  -e ADMIN_PASSWORD='replace-me' \
  -e SECRET_KEY='replace-with-a-long-random-secret' \
  -e UPLOAD_TOKEN='replace-with-a-secret-token' \
  -e APP_BASE_URL='https://receipts.example.com' \
  -v receipt-upload-data:/app/data \
  receipt-upload
```

Uploaded PDFs and SQLite data are stored under `/app/data`.

## Make Targets

```bash
make install
make sync
make run
make check
make clean
```
