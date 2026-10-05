# receipt-upload

`receipt-upload` is a small Go receipt collection portal for one admin user.

The admin logs in, manages cardholders and stores, and reviews uploaded receipts. Cardholders do not have accounts. They receive a secret upload link, choose their name, enter receipt details, upload one or more receipt images, and the app creates one compressed PDF per expense.

## Features

- Go `net/http` web app.
- Single admin login configured by an explicit environment-style config file.
- Admin-managed secret cardholder upload URL: `/upload/{UPLOAD_TOKEN}`.
- Cardholder and store management from the admin portal.
- Multiple store checkboxes per receipt.
- Multiple receipt images per upload, resized and merged into one PDF.
- Uploads stream original images to a durable queue and return before conversion.
- A separate worker polls `data/queue` every second and converts one receipt at a time.
- Admin conversion jobs show queued, processing, and failed receipts, with retry controls.
- Already-prepared RGB JPEGs up to 1600 pixels and 512 KB are embedded in PDFs without a second compression pass.
- Server-side image resizing and JPEG compression before PDF generation.
- Uploaded PDFs stored on disk.
- Receipt metadata stored in SQLite.
- Admin receipt list with categorized downloads and a receipt graveyard for restore or permanent deletion.
- App disk usage shown on the admin dashboard.
- Failed admin login protection with IP bans.
- CLI commands to list and remove banned IPs.
- Dockerfile for VPS deployment.

## Requirements

- Go toolchain.
- A C compiler for the SQLite driver.

No external command or image conversion package is required. Go compiles image decoding, resizing, PDF generation, and SQLite support into `receipt-upload` itself.

## Install

From the project directory, one command installs the app and all of its Go dependencies:

```bash
make install
```

This is equivalent to:

```bash
go install .
```

No separate PDF dependency setup is needed. Initialize and start the installed app with:

```bash
receipt-upload init
# Edit ADMIN_PASSWORD in ./config/.env, then:
receipt-upload serve
# In a second terminal, from the same directory:
receipt-upload worker
```

The developer documentation is served at `http://localhost:8725/`. The admin-only login portal is at `http://localhost:8725/admin/login`. To select another bind address or port:

```bash
receipt-upload serve --config ./runtime/app.env --host 0.0.0.0 --port 8725
```

`receipt-upload --config ./runtime/app.env --host 0.0.0.0 --port 8725` also works as shorthand for `receipt-upload serve`.

## First-Time Setup

Initialize the configuration, SQLite database, and receipt storage:

```bash
receipt-upload init
```

This creates `./config/.env`, `./data/main.sqlite`, and `./data/receipts/`.

Edit `ADMIN_PASSWORD` before real use, then validate the file without starting the server:

```bash
receipt-upload config check
```

Start the app only after validation succeeds:

```bash
receipt-upload serve
# In another terminal, using the same working directory and configuration:
receipt-upload worker
```

To initialize only one part, use `receipt-upload config init` or
`receipt-upload database init`.

Existing config files can be edited with the CLI:

```bash
receipt-upload set-username --config ./runtime/app.env admin
receipt-upload set-password --config ./runtime/app.env
receipt-upload set-config --config ./runtime/app.env APP_BASE_URL https://receipts.example.com
```

## CLI Reference

```bash
receipt-upload init
receipt-upload init --force
receipt-upload init --config ./runtime/app.env
receipt-upload database init
receipt-upload --config ./runtime/app.env
receipt-upload serve --config ./runtime/app.env
receipt-upload worker --config ./runtime/app.env
receipt-upload serve --config ./runtime/app.env --host 0.0.0.0 --port 8725
receipt-upload config init --path ./runtime/app.env
receipt-upload config init --path ./runtime/app.env --force
receipt-upload config check --config ./runtime/app.env
receipt-upload set-username --config ./runtime/app.env admin
receipt-upload set-password --config ./runtime/app.env
receipt-upload set-password --config ./runtime/app.env 'replace-this-password'
receipt-upload set-config --config ./runtime/app.env APP_BASE_URL https://receipts.example.com
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
8. Create expense categories alongside locations, then optionally select a category when downloading.
9. Delete receipts to move them to the receipt graveyard. Restore them or permanently delete them from there.

The admin dashboard shows how much disk space the app is using under `./data`.
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

Each submission is acknowledged once the originals and metadata are saved. PDF conversion runs later in the worker. Unsupported or corrupt images appear as failed jobs in administration; their originals remain available for retry.

## Receipt Storage

The app stores data under `./data`.

```text
data/
  main.sqlite
  queue/
    <job-id>/
      000000  # original image bytes, in upload order
      000001
    worker.lock
  receipts/
    <generated-id>.pdf
```

SQLite stores receipt metadata, selected stores, timestamps, deletion status, PDF paths, and conversion job status. The worker processes only directories registered as queued jobs in SQLite; arbitrary files dropped in the directory are not treated as receipts.

Run `serve` and `worker` as two supervised processes using the same working directory and config file. Only one worker may run for a data directory. Interrupted processing jobs return to the queue on worker restart. PDFs are published with an atomic rename, and receipt size and completion status are committed together, so restarting does not duplicate receipts. Original images are deleted after successful conversion; failed jobs retain them. Receipt dates and cardholder/location names are captured at upload time. The admin job panel refreshes every five seconds; refresh the dashboard to see newly completed PDFs.

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

The default Docker command supervises two separate processes: the web server and conversion worker. If either exits, the container exits so a restart policy can restart both. Use `--restart unless-stopped` for persistent deployments. Overriding the Docker command with `receipt-upload serve` starts only the web server, so run a separate worker container sharing the same data and config mounts.

Original queued images, PDFs, and SQLite data are stored under `/app/data` in Docker.

## Make Targets

```bash
make install
make sync
make run
make worker  # second terminal
make check
make clean
```


### Receipt export names and graveyard

Downloads use `MMDDYY-vendor-price-description-category-location.pdf`, for example
`092626-sams-19.08-milk-blank-split.pdf`. The date is the upload date in
America/Chicago, and the price has two decimal places. Names are lowercase with
hyphens and apostrophes removed from each field. Other punctuation becomes
spaces, and repeated spaces are collapsed. Every filename contains exactly five
hyphens, separating the six fields.
A single selected location uses its name; two or more use `split`.
Missing descriptions, categories, or locations use literal `blank`.

Expense categories can be added and removed in administration. The category
selection applies to that download and is optional.

Delete moves a receipt to the graveyard without removing its PDF. Permanent
deletion is available only for receipts in the graveyard and asks for confirmation.
Existing archived receipts appear in the active list after upgrading; their PDFs
are preserved. Database changes are applied automatically at server startup.

Failed conversion jobs offer Retry and Clear. Clear removes the failed upload and
its queued source files; queued, processing, and completed jobs cannot be cleared.
