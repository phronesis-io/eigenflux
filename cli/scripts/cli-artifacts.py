#!/usr/bin/env python3
"""Generate and verify checksums for the complete CLI release, after any signing."""
import argparse
import base64
import binascii
import hashlib
import json
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


def verify_release(build):
    # The updater verifies Ed25519 authenticity; publication preflight ensures
    # the signed manifest describes the complete, final (including signed) build.
    manifest = json.loads((build / 'release.json').read_text(encoding='utf-8'))
    version = (build / 'version.txt').read_text(encoding='ascii').strip()
    if not re.fullmatch(r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', version):
        raise ValueError('invalid CLI release version')
    if manifest.get('version') != version or set(manifest.get('artifacts', {})) != set(BINARIES):
        raise ValueError('CLI release manifest version or platform set differs from build')
    try:
        signature = base64.b64decode(manifest.get('signature', ''), validate=True)
    except (binascii.Error, TypeError, ValueError) as error:
        raise ValueError('invalid CLI release signature encoding') from error
    if len(signature) != 64:
        raise ValueError('CLI release manifest is unsigned')
    for name, value in verify(build).items():
        artifact = manifest['artifacts'][name]
        if artifact.get('sha256') != value or artifact.get('size') != (build / name).stat().st_size:
            raise ValueError(f'CLI release manifest differs from final binary: {name}')


def verify_release_public(build, base_url):
    verify_release(build)
    query = '?verify=' + uuid.uuid4().hex
    with urlopen(base_url.rstrip('/') + '/release.json' + query, timeout=30) as response:
        if response.read(1024 * 1024 + 1) != (build / 'release.json').read_bytes():
            raise ValueError('published CLI release manifest differs from build')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("generate", "verify", "list", "verify-public", "verify-release", "verify-release-public"))
    parser.add_argument("build", type=Path)
    parser.add_argument("--base-url")
    args = parser.parse_args()
    if args.command == "generate":
        generate(args.build)
    elif args.command == "verify-release":
        verify_release(args.build)
    elif args.command == "verify-release-public":
        if not args.base_url:
            parser.error('verify-release-public requires --base-url')
        verify_release_public(args.build, args.base_url)
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
