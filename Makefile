.PHONY: install sync run check clean

install:
	go install .

sync:
	go mod download

run:
	go run . serve --host 0.0.0.0 --port 8725

check:
	gofmt -w main.go
	go test ./...

clean:
	rm -rf receipt-upload

docker:
	docker build -t receipt-upload . && docker run --rm \
	       	-p 8725:8725 \
		-v $(PWD)/config:/app/config \
		-v $(PWD)/data:/app/data \
	       	receipt-upload 
