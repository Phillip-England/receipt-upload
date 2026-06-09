FROM rust:1-bookworm AS builder

WORKDIR /app
COPY Cargo.toml Cargo.lock ./
COPY src ./src
RUN cargo build --release

FROM debian:bookworm-slim

ENV DATA_DIR=/app/data

WORKDIR /app
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/target/release/receipt-upload /usr/local/bin/receipt-upload

EXPOSE 8725
VOLUME ["/app/data"]

CMD ["receipt-upload", "serve", "--host", "0.0.0.0", "--port", "8725"]
