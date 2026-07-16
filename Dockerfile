FROM rust:1.88
WORKDIR /app
COPY . .
RUN cargo install --path .
EXPOSE 8725
CMD ["receipt-upload"]

