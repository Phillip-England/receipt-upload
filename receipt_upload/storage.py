from __future__ import annotations

import os
import shutil
import tempfile
import uuid
from pathlib import Path

from fastapi import UploadFile

from receipt_upload.imagemagick import convert_to_pdf


def directory_size(path: Path) -> int:
    if not path.exists():
        return 0
    total = 0
    for root, _, files in os.walk(path):
        for filename in files:
            try:
                total += (Path(root) / filename).stat().st_size
            except FileNotFoundError:
                continue
    return total


def human_bytes(size: int) -> str:
    units = ["B", "KB", "MB", "GB", "TB"]
    value = float(size)
    for unit in units:
        if value < 1024 or unit == units[-1]:
            return f"{value:.1f} {unit}" if unit != "B" else f"{int(value)} B"
        value /= 1024
    return f"{size} B"


async def save_receipt_pdf(
    files: list[UploadFile],
    upload_dir: Path,
    max_upload_bytes: int,
    auto_install_imagemagick: bool,
) -> tuple[Path, int, list[str]]:
    if not files or not any(file.filename for file in files):
        raise ValueError("Please choose at least one receipt image.")
    upload_dir.mkdir(parents=True, exist_ok=True)
    output_path = upload_dir / f"{uuid.uuid4().hex}.pdf"
    original_filenames: list[str] = []
    total_bytes = 0
    with tempfile.TemporaryDirectory() as tmp:
        input_paths: list[str] = []
        for index, file in enumerate(files):
            if not file.filename:
                continue
            original_filenames.append(file.filename)
            suffix = Path(file.filename).suffix.lower() or ".upload"
            input_path = Path(tmp) / f"{index}{suffix}"
            with input_path.open("wb") as handle:
                while chunk := await file.read(1024 * 1024):
                    total_bytes += len(chunk)
                    if total_bytes > max_upload_bytes:
                        raise ValueError("Upload is too large.")
                    handle.write(chunk)
            input_paths.append(str(input_path))
        if not input_paths:
            raise ValueError("Please choose at least one receipt image.")
        if len(input_paths) == 1 and Path(input_paths[0]).suffix.lower() == ".pdf":
            shutil.copyfile(input_paths[0], output_path)
        else:
            convert_to_pdf(input_paths, str(output_path), auto_install_imagemagick)
    return output_path, output_path.stat().st_size, original_filenames
