#!/usr/bin/env python3
"""Build and verify deterministic complete CLI ZIP packages before signing."""
import argparse
import hashlib
from pathlib import Path
import stat
import sys
import zipfile

PLATFORMS = tuple((os_name, arch) for os_name in ("linux", "darwin", "windows") for arch in ("amd64", "arm64"))
HELPER = "EigenFlux Notifications.app"
MAX_FILES = 4096
MAX_FILE_BYTES = 128 << 20
MAX_EXPANDED_BYTES = 512 << 20
MAX_ARCHIVE_BYTES = 256 << 20


def binary_name(os_name, arch):
    return f"eigenflux-{os_name}-{arch}" + (".exe" if os_name == "windows" else "")


def package_names():
    return tuple(binary_name(os_name, arch) + ".zip" for os_name, arch in PLATFORMS)


def source_files(build, helpers, os_name, arch):
    executable = "eigenflux.exe" if os_name == "windows" else "eigenflux"
    result = [(executable, build / binary_name(os_name, arch), 0o755)]
    if os_name == "darwin":
        helper = helpers / f"darwin-{arch}" / HELPER
        for relative in ("Contents/Info.plist", "Contents/MacOS/EigenFluxNotifications", "Contents/Resources/EigenFlux.icns"):
            if not (helper / relative).is_file():
                raise ValueError(f"missing complete macOS notification helper: {helper / relative}")
        if helper.is_symlink():
            raise ValueError("notification helper must not be a symlink")
        for source in sorted(helper.rglob("*")):
            if source.is_symlink():
                raise ValueError(f"symlink in notification helper: {source}")
            if source.is_dir():
                continue
            result.append((HELPER + "/" + source.relative_to(helper).as_posix(), source, 0o755 if source.stat().st_mode & 0o111 else 0o644))
    if len(result) > MAX_FILES or sum(source.stat().st_size for _, source, _ in result) > MAX_EXPANDED_BYTES:
        raise ValueError("CLI package exceeds expanded limits")
    for name, source, _ in result:
        if not source.is_file() or source.is_symlink() or source.stat().st_size > MAX_FILE_BYTES:
            raise ValueError(f"invalid CLI package source: {source}")
        if not name.isascii() or any(character in name for character in "\\:\x00"):
            raise ValueError("non-portable CLI package path")
    return result


def generate(build, helpers):
    for os_name, arch in PLATFORMS:
        files = source_files(build, helpers, os_name, arch)
        target = build / (binary_name(os_name, arch) + ".zip")
        temporary = target.with_suffix(".zip.tmp")
        try:
            with zipfile.ZipFile(temporary, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=6, allowZip64=False) as archive:
                for name, source, mode in sorted(files):
                    entry = zipfile.ZipInfo(name, (2020, 1, 1, 0, 0, 0))
                    entry.create_system = 3
                    entry.external_attr = (stat.S_IFREG | mode) << 16
                    entry.compress_type = zipfile.ZIP_DEFLATED
                    archive.writestr(entry, source.read_bytes())
            if temporary.stat().st_size > MAX_ARCHIVE_BYTES:
                raise ValueError("CLI package exceeds archive limit")
            temporary.replace(target)
        finally:
            temporary.unlink(missing_ok=True)


def verify(build):
    for os_name, arch in PLATFORMS:
        target = build / (binary_name(os_name, arch) + ".zip")
        if target.stat().st_size <= 0 or target.stat().st_size > MAX_ARCHIVE_BYTES:
            raise ValueError(f"invalid CLI package size: {target.name}")
        with zipfile.ZipFile(target) as archive:
            names = archive.namelist()
            if not names or len(names) > MAX_FILES or len(set(name.lower() for name in names)) != len(names):
                raise ValueError(f"invalid CLI package entries: {target.name}")
            executable = "eigenflux.exe" if os_name == "windows" else "eigenflux"
            for entry in archive.infolist():
                parts = entry.filename.split("/")
                if not entry.filename.isascii() or any(part in ("", ".", "..") for part in parts) or any(character in entry.filename for character in "\\:\x00"):
                    raise ValueError("unsafe CLI package path")
                if entry.filename != executable and not (os_name == "darwin" and entry.filename.startswith(HELPER + "/")):
                    raise ValueError("unexpected CLI package file")
                if stat.S_IFMT(entry.external_attr >> 16) != stat.S_IFREG or entry.file_size > MAX_FILE_BYTES:
                    raise ValueError("non-regular or oversized CLI package file")
            if sum(entry.file_size for entry in archive.infolist()) > MAX_EXPANDED_BYTES:
                raise ValueError("CLI package exceeds expanded limit")
            if archive.testzip() is not None:
                raise ValueError("CLI package CRC mismatch")
            if executable not in names or hashlib.sha256(archive.read(executable)).digest() != hashlib.sha256((build / binary_name(os_name, arch)).read_bytes()).digest():
                raise ValueError("packaged CLI differs from bare signed artifact")
            if os_name == "darwin":
                required = (HELPER + "/Contents/" + relative for relative in ("Info.plist", "MacOS/EigenFluxNotifications", "Resources/EigenFlux.icns"))
                if any(name not in names for name in required):
                    raise ValueError("macOS package lacks notification helper")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("generate", "verify", "list"))
    parser.add_argument("build", type=Path)
    parser.add_argument("--helpers", type=Path)
    args = parser.parse_args()
    if args.command == "generate":
        if args.helpers is None:
            parser.error("generate requires --helpers with darwin-{arch}/" + HELPER)
        generate(args.build, args.helpers)
    verify(args.build)
    if args.command == "list":
        print("\n".join(package_names()))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, zipfile.BadZipFile) as error:
        sys.exit(str(error))
