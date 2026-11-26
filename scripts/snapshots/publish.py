#!/usr/bin/env python3
"""Publish only to a new immutable snapshot prefix; never touch production."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
from verify import check_native, read_public


def validate_prefix(prefix):
    if not re.fullmatch(r"snapshots/[0-9a-f]{40}/[0-9a-f]{40}", prefix):
        raise ValueError("Refusing a non-snapshot publication prefix")


def publish(public):
    provenance = json.loads((public / "provenance.json").read_text())
    prefix = provenance["prefix"]
    validate_prefix(prefix)
    if provenance["base_url"] != "https://cdn.eigenflux.ai/" + prefix:
        raise ValueError("Unexpected public snapshot origin")
    hashes = json.loads((public / "checksums.json").read_text())
    publish_hashes = {**hashes, "checksums.json": hashlib.sha256((public / "checksums.json").read_bytes()).hexdigest()}
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
    def publish_artifact(path):
        relative = str(path.relative_to(public))
        content_type = "text/plain; charset=utf-8" if path.suffix in (".md", ".sh", ".txt", ".ps1", ".sha256") else "application/octet-stream"
        if path.suffix == ".json":
            content_type = "application/json"
        aws("s3api", "put-object", "--bucket", bucket, "--key", prefix + "/" + relative,
            "--body", str(path), "--if-none-match", "*",
            "--cache-control", "public,max-age=31536000,immutable", "--content-type", content_type)
        actual = hashlib.sha256(read_public(provenance["base_url"], relative)).hexdigest()
        if actual != publish_hashes[relative]:
            raise ValueError("Public artifact verification failed: " + relative)
    entry = public / "install.md"
    for path in sorted(files):
        if path != entry:
            publish_artifact(path)
    check_native(provenance, hashes)
    publish_artifact(entry)
    print("Verified installation entry: " + provenance["base_url"] + "/install.md")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("public", type=Path)
    publish(parser.parse_args().public.resolve())
