.PHONY: install sync run clean

install:
	uv tool install --force .

sync:
	uv sync

run:
	uv run receipt-upload serve --host 0.0.0.0 --port 8725

clean:
	rm -rf .venv receipt_upload.egg-info build dist
