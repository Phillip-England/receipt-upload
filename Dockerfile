FROM golang:1.24
WORKDIR /app
COPY . .
RUN go build -o /usr/local/bin/receipt-upload .
EXPOSE 8725
CMD ["receipt-upload"]
