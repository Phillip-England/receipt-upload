FROM golang:1.24
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY . .
RUN go build -o /usr/local/bin/receipt-upload . && chmod +x /app/start.sh
EXPOSE 8725
CMD ["/app/start.sh"]
