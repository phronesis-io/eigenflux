#!/usr/bin/env python3
"""Publish only to a new immutable snapshot prefix; never touch production."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.request


def validate_prefix(prefix):
    if not re.fullmatch(r"snapshots/[0-9a-f]{40}/[0-9a-f]{40}", prefix):
        raise ValueError("Refusing a non-snapshot publication prefix")


def publish(public):
    provenance = json.loads((public / "provenance.json").read_text())
    prefix = provenance["prefix"]
    validate_prefix(prefix)
    hashes = json.loads((public / "checksums.json").read_text())
    files = [p for p in public.rglob("*") if p.is_file()]
    if any(p.is_symlink() for p in public.rglob("*")):
        raise ValueError("Refusing symlinked publication files")
    for relative, expected in hashes.items():
        if relative.startswith("/") or ".." in Path(relative).parts:
            raise ValueError("Unsafe artifact path")
        if hashlib.sha256((public / relative).read_bytes()).hexdigest() != expected:
            raise ValueError("Artifact changed: " + relative)
    if {str(p.relative_to(public)) for p in files} != set(hashes) | {"checksums.json"}:
        raise ValueError("Unlisted artifact present")
    env = dict(os.environ, AWS_ACCESS_KEY_ID=os.environ["R2_ACCESS_KEY_ID"], AWS_SECRET_ACCESS_KEY=os.environ["R2_SECRET_ACCESS_KEY"])
    bucket = os.environ["R2_BUCKET"]
    def aws(*args):
        return subprocess.run(["aws", "--endpoint-url", os.environ["R2_ENDPOINT"], *args], env=env,
                              text=True, capture_output=True, check=True).stdout
    listed = json.loads(aws("s3api", "list-objects-v2", "--bucket", bucket, "--prefix", prefix + "/", "--max-keys", "1"))
    if listed.get("KeyCount", 0) or listed.get("Contents"):
        raise ValueError("Snapshot prefix already exists; create a new distribution commit")
    # Publish the entry last; no caller should receive a partially published entry.
    files.sort(key=lambda p: (p.name == "install.md", str(p)))
    for path in files:
        relative = str(path.relative_to(public))
        content_type = "text/plain; charset=utf-8" if path.suffix in (".md", ".sh", ".txt", ".ps1", ".sha256") else "application/octet-stream"
        if path.suffix == ".json":
            content_type = "application/json"
        aws("s3", "cp", str(path), "s3://" + bucket + "/" + prefix + "/" + relative,
            "--cache-control", "public,max-age=31536000,immutable", "--content-type", content_type, "--only-show-errors")
    for relative, expected in hashes.items():
        with urllib.request.urlopen(provenance["base_url"] + "/" + relative, timeout=60) as response:
            actual = hashlib.sha256(response.read()).hexdigest()
        if actual != expected:
            raise ValueError("Public artifact verification failed: " + relative)
    print("Verified installation entry: " + provenance["base_url"] + "/install.md")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("public", type=Path)
    publish(parser.parse_args().public.resolve())
