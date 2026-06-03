from __future__ import annotations

import json
import os
from dataclasses import dataclass
from pathlib import Path


APP_NAME = "receipt-upload"
CONFIG_ENV = "RECEIPT_UPLOAD_CONFIG"
DEFAULT_ENV = {
    "ADMIN_USERNAME": "admin",
    "ADMIN_PASSWORD": "password",
    "SECRET_KEY": "dev-secret-change-me",
    "UPLOAD_TOKEN": "dev-upload-token",
    "APP_BASE_URL": "http://localhost:8725",
    "DATA_DIR": "./data",
    "MAX_UPLOAD_MB": "50",
    "AUTO_INSTALL_IMAGEMAGICK": "true",
}


def default_config_path() -> Path:
    configured_path = os.environ.get(CONFIG_ENV)
    if configured_path:
        return Path(configured_path).expanduser()

    if os.name == "nt" and os.environ.get("APPDATA"):
        return Path(os.environ["APPDATA"]) / APP_NAME / "config.json"

    config_home = os.environ.get("XDG_CONFIG_HOME")
    if config_home:
        return Path(config_home).expanduser() / APP_NAME / "config.json"

    return Path.home() / ".config" / APP_NAME / "config.json"


def load_saved_config() -> dict[str, str]:
    path = default_config_path()
    if not path.exists():
        return {}
    try:
        values = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise RuntimeError(f"Could not read {path}: {exc}") from exc
    if not isinstance(values, dict):
        raise RuntimeError(f"{path} must contain a JSON object.")
    return {key: str(value) for key, value in values.items() if key in DEFAULT_ENV}


def save_config_value(name: str, value: str) -> Path:
    if name not in DEFAULT_ENV:
        raise ValueError(f"Unsupported configuration key: {name}")
    values = load_saved_config()
    values[name] = value
    path = default_config_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(values, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    path.chmod(0o600)
    return path


def _configured_value(saved_config: dict[str, str], name: str) -> str:
    return os.environ.get(name, saved_config.get(name, DEFAULT_ENV[name]))


def _bool_value(saved_config: dict[str, str], name: str) -> bool:
    value = _configured_value(saved_config, name)
    default = DEFAULT_ENV[name].lower() in {"1", "true", "yes", "on"}
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

    @property
    def uses_default_admin_credentials(self) -> bool:
        return (
            self.admin_username == DEFAULT_ENV["ADMIN_USERNAME"]
            or self.admin_password == DEFAULT_ENV["ADMIN_PASSWORD"]
        )


def load_settings() -> Settings:
    saved_config = load_saved_config()
    data_dir = Path(_configured_value(saved_config, "DATA_DIR")).resolve()
    upload_dir = data_dir / "receipts"
    max_upload_mb = int(_configured_value(saved_config, "MAX_UPLOAD_MB"))
    return Settings(
        admin_username=_configured_value(saved_config, "ADMIN_USERNAME"),
        admin_password=_configured_value(saved_config, "ADMIN_PASSWORD"),
        secret_key=_configured_value(saved_config, "SECRET_KEY"),
        upload_token=_configured_value(saved_config, "UPLOAD_TOKEN"),
        app_base_url=_configured_value(saved_config, "APP_BASE_URL"),
        data_dir=data_dir,
        upload_dir=upload_dir,
        max_upload_bytes=max_upload_mb * 1024 * 1024,
        auto_install_imagemagick=_bool_value(saved_config, "AUTO_INSTALL_IMAGEMAGICK"),
    )
