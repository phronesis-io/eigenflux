#!/usr/bin/env python3
"""Generate and verify checksums for the complete CLI release, after any signing."""
import argparse
import hashlib
from pathlib import Path
import re
import sys
from urllib.request import urlopen
import uuid

BINARIES = tuple(
    f"eigenflux-{os}-{arch}" + (".exe" if os == "windows" else "")
    for os in ("linux", "darwin", "windows") for arch in ("amd64", "arm64")
)


def digest(stream):
    result = hashlib.sha256()
    for chunk in iter(lambda: stream.read(1024 * 1024), b""):
        result.update(chunk)
    return result.hexdigest()


def checksums(build):
    result = {}
    for name in BINARIES:
        path = build / name
        if not path.is_file() or path.stat().st_size == 0:
            raise ValueError(f"missing or empty CLI binary: {path}")
        with path.open("rb") as stream:
            result[name] = digest(stream)
    return result


def generate(build):
    for name, value in checksums(build).items():
        (build / (name + ".sha256")).write_text(value + "\n", encoding="ascii")


def verify(build):
    expected = checksums(build)
    for name, value in expected.items():
        actual = (build / (name + ".sha256")).read_text(encoding="ascii").strip()
        if not re.fullmatch(r"[0-9a-f]{64}", actual) or actual != value:
            raise ValueError(f"SHA256 mismatch or invalid checksum: {name}")
    return expected


def verify_public(build, base_url):
    # Check the public delivery path against local artifacts, not just against
    # a remote checksum that could belong to the same stale release.
    for name, value in verify(build).items():
        url = base_url.rstrip("/") + "/" + name
        query = "?verify=" + uuid.uuid4().hex
        with urlopen(url + ".sha256" + query, timeout=30) as response:
            checksum = response.read(1024).decode("ascii").strip()
        if checksum != value:
            raise ValueError(f"published checksum differs from build: {name}")
        with urlopen(url + query, timeout=30) as response:
            if digest(response) != value:
                raise ValueError(f"published binary differs from build: {name}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("generate", "verify", "list", "verify-public"))
    parser.add_argument("build", type=Path)
    parser.add_argument("--base-url")
    args = parser.parse_args()
    if args.command == "generate":
        generate(args.build)
    elif args.command == "verify-public":
        if not args.base_url:
            parser.error("verify-public requires --base-url")
        verify_public(args.build, args.base_url)
    else:
        verify(args.build)
        if args.command == "list":
            for name in BINARIES:
                print(name)
                print(name + ".sha256")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError) as error:
        sys.exit(str(error))
