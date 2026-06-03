from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path


def _load_dotenv() -> None:
    env_path = Path(".env")
    if not env_path.exists():
        return
    for raw_line in env_path.read_text().splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip().strip('"').strip("'")
        os.environ.setdefault(key, value)


def _bool_env(name: str, default: bool) -> bool:
    value = os.environ.get(name)
    if value is None:
        return default
    return value.lower() in {"1", "true", "yes", "on"}


@dataclass(frozen=True)
class Settings:
    admin_username: str
    admin_password: str
    secret_key: str
    upload_token: str
    app_base_url: str
    data_dir: Path
    upload_dir: Path
    max_upload_bytes: int
    auto_install_imagemagick: bool

    @property
    def upload_url(self) -> str:
        return f"{self.app_base_url.rstrip('/')}/upload/{self.upload_token}"


def load_settings() -> Settings:
    _load_dotenv()
    data_dir = Path(os.environ.get("DATA_DIR", "./data")).resolve()
    upload_dir = data_dir / "receipts"
    max_upload_mb = int(os.environ.get("MAX_UPLOAD_MB", "50"))
    return Settings(
        admin_username=os.environ.get("ADMIN_USERNAME", "admin"),
        admin_password=os.environ.get("ADMIN_PASSWORD", "change-this-password"),
        secret_key=os.environ.get("SECRET_KEY", "dev-secret-change-me"),
        upload_token=os.environ.get("UPLOAD_TOKEN", "dev-upload-token"),
        app_base_url=os.environ.get("APP_BASE_URL", "http://localhost:8725"),
        data_dir=data_dir,
        upload_dir=upload_dir,
        max_upload_bytes=max_upload_mb * 1024 * 1024,
        auto_install_imagemagick=_bool_env("AUTO_INSTALL_IMAGEMAGICK", True),
    )
