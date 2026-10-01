FROM golang:1.24
WORKDIR /app
COPY . .
RUN go build -o /usr/local/bin/receipt-upload . && chmod +x /app/start.sh
EXPOSE 8725
CMD ["/app/start.sh"]
