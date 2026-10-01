.PHONY: install sync run worker check clean

install:
	go install .

sync:
	go mod download

run:
	go run . serve --host 0.0.0.0 --port 8725

worker:
	go run . worker

check:
	gofmt -w *.go
	go test ./...

clean:
	rm -rf receipt-upload

docker:
	docker build -t receipt-upload . && docker run --rm \
	       	-p 8725:8725 \
		-v $(CURDIR)/config:/app/config \
		-v $(CURDIR)/data:/app/data \
	       	receipt-upload 
