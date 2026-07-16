.PHONY: install sync run check clean

install:
	cargo install --locked --path . --force

sync:
	cargo fetch

run:
	cargo run -- serve --host 0.0.0.0 --port 8725

check:
	cargo fmt --check
	cargo check

clean:
	rm -rf target

docker:
	docker build -t receipt-upload . && docker run --rm \
	       	-p 8725:8725 \
		-v $(pwd)/config:/app/config \
		-v $(pwd)/data:/app/data \
	       	receipt-upload 
