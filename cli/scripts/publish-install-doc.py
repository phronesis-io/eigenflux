#!/usr/bin/env python3
"""Publish the canonical installation entry after a verified Skills release."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlsplit
from urllib.request import urlopen

KEY = "skills/latest/install.md"
CONTENT_TYPE = "text/markdown; charset=utf-8"
CACHE_CONTROL = "no-store"
ATTEMPTS = 3


def aws(*args):
    try:
        result = subprocess.run(
            ["aws", "--endpoint-url", os.environ["R2_ENDPOINT"], "--region", "auto",
             "--cli-connect-timeout", "10", "--cli-read-timeout", "20",
             "s3api", *args, "--bucket", os.environ["R2_BUCKET"], "--output", "json"],
            check=True, capture_output=True, text=True, timeout=45,
        )
    except subprocess.CalledProcessError as error:
        raise RuntimeError(f"R2 {args[0]} failed: {error.stderr.strip()}") from error
    except subprocess.TimeoutExpired as error:
        raise RuntimeError(f"R2 {args[0]} timed out") from error
    return json.loads(result.stdout or "{}")


def publish(source):
    expected = source.read_bytes()
    if not expected:
        raise ValueError("canonical skills/install.md is empty")
    for name in ("R2_ENDPOINT", "R2_BUCKET", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"):
        if not os.environ.get(name):
            raise ValueError(f"{name} is required")
    base = os.environ.get("EIGENFLUX_CDN", "https://cdn.eigenflux.ai").rstrip("/")
    parsed = urlsplit(base)
    if parsed.scheme not in ("https", "http") or not parsed.netloc or parsed.query or parsed.fragment:
        raise ValueError("EIGENFLUX_CDN must be an HTTP(S) base URL without query or fragment")
    url = f"{base}/{KEY}"

    with tempfile.TemporaryDirectory(prefix="eigenflux-install-doc-") as temp:
        snapshot = Path(temp) / "install.md"
        snapshot.write_bytes(expected)
        aws("put-object", "--key", KEY, "--body", str(snapshot),
            "--content-type", CONTENT_TYPE, "--cache-control", CACHE_CONTROL)
        downloaded = Path(temp) / "origin-install.md"
        for attempt in range(1, ATTEMPTS + 1):
            try:
                metadata = aws("get-object", "--key", KEY, str(downloaded))
                if (metadata.get("ContentType") != CONTENT_TYPE
                        or metadata.get("CacheControl") != CACHE_CONTROL
                        or downloaded.read_bytes() != expected):
                    raise RuntimeError("R2 installation document content or metadata differs from source")
                # Verify the exact public entry URL; a cache-busting query hides stale edges.
                with urlopen(url, timeout=15) as response:
                    cache_directives = {value.strip().lower()
                                        for value in response.headers.get("Cache-Control", "").split(",")}
                    if "no-store" not in cache_directives:
                        raise RuntimeError("CDN installation document must return Cache-Control: no-store")
                    if response.read(len(expected) + 1) != expected:
                        raise RuntimeError("CDN installation document differs from source")
                print(f"Verified installation document: {url}", flush=True)
                return
            except (OSError, RuntimeError, ValueError) as error:
                if attempt == ATTEMPTS:
                    raise RuntimeError(f"Installation document verification failed after {ATTEMPTS} attempts: {error}") from error
                print(f"Installation document verification attempt {attempt} failed: {error}; retrying", file=sys.stderr)
                time.sleep(5)


def main():
    source = Path(__file__).resolve().parents[2] / "skills/install.md"
    try:
        publish(source)
    except (OSError, RuntimeError, ValueError) as error:
        print(f"Installation document publish failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
