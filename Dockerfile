FROM rust:1.88 AS builder
WORKDIR /app
COPY . .
RUN cargo install --path .
EXPOSE 8725
CMD ["./receipt-upload"]

