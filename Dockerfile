FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    DATA_DIR=/app/data

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends imagemagick ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY pyproject.toml README.md ./
COPY receipt_upload ./receipt_upload

RUN uv sync --no-dev

EXPOSE 8725
VOLUME ["/app/data"]

CMD ["uv", "run", "receipt-upload", "serve", "--host", "0.0.0.0", "--port", "8725"]
