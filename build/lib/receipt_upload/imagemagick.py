from __future__ import annotations

import shutil
import subprocess


class ImageMagickUnavailable(RuntimeError):
    pass


def imagemagick_command() -> str | None:
    return shutil.which("magick") or shutil.which("convert")


def ensure_imagemagick(auto_install: bool) -> str:
    command = imagemagick_command()
    if command:
        return command
    if auto_install:
        _try_install()
        command = imagemagick_command()
        if command:
            return command
    raise ImageMagickUnavailable(
        "ImageMagick is not installed. Install it or run the Docker image, which includes ImageMagick."
    )


def _try_install() -> None:
    installers = [
        (["apt-get", "update"], ["apt-get", "install", "-y", "imagemagick"]),
        (["apk", "add", "--no-cache", "imagemagick"],),
        (["brew", "install", "imagemagick"],),
    ]
    for command_group in installers:
        if not shutil.which(command_group[0][0]):
            continue
        try:
            for command in command_group:
                subprocess.run(command, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            return
        except (subprocess.CalledProcessError, PermissionError):
            continue


def convert_to_pdf(inputs: list[str], output: str, auto_install: bool) -> None:
    command = ensure_imagemagick(auto_install)
    subprocess.run([command, *inputs, output], check=True)
