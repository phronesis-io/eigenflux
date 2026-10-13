#!/usr/bin/env python3
"""Version and atomically replace the sole public shell installer."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time
from urllib.request import urlopen

CDN = "https://cdn.eigenflux.ai"
LATEST_KEY = "installers/latest/install.sh"
CONTENT_TYPE = "text/plain; charset=utf-8"
DEVELOPMENT_VERSION = "0.0.0-dev"
ATTEMPTS = 3
VERSION = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")
COMMIT = re.compile(r"[0-9a-f]{40}")


def aws(*args):
    result = subprocess.run(
        ["aws", "--endpoint-url", os.environ["R2_ENDPOINT"], "--region", "auto",
         "--cli-connect-timeout", "10", "--cli-read-timeout", "20",
         "s3api", *args, "--bucket", os.environ["R2_BUCKET"], "--output", "json"],
        check=True, capture_output=True, text=True, timeout=45,
    )
    return json.loads(result.stdout or "{}")


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], timeout=30)


def field(content, name):
    matches = re.findall(rb'^' + name.encode() + rb'="([^"\r\n]+)"$', content, re.M)
    if len(matches) != 1:
        raise ValueError(f"Expected exactly one literal {name} assignment")
    return matches[0].decode("ascii")


def version_tuple(version):
    if not VERSION.fullmatch(version):
        raise ValueError(f"Invalid installer version: {version}")
    return tuple(map(int, version.split(".")))


def render(source, version, commit):
    version_tuple(version)
    if not COMMIT.fullmatch(commit):
        raise ValueError("Invalid installer source commit")
    for name, value in (("INSTALLER_VERSION", version), ("INSTALLER_SOURCE_COMMIT", commit)):
        field(source, name)
        source = re.sub(rb'^' + name.encode() + rb'="[^"\r\n]+"$',
                        f'{name}="{value}"'.encode(), source, flags=re.M)
    return source


def source_identity(root, source):
    if source != git(root, "show", "HEAD:static/install.sh"):
        raise ValueError("Installer must match the committed checkout")
    # Identify when these bytes entered main, not a parallel branch whose
    # history may exclude an earlier release. Unrelated main commits are skipped.
    commit = git(root, "log", "--first-parent", "-1", "--format=%H", "--", "static/install.sh").decode().strip()
    if not COMMIT.fullmatch(commit):
        raise ValueError("Cannot resolve installer source commit")
    if git(root, "show", f"{commit}:static/install.sh") != source:
        raise ValueError("Installer source commit does not reproduce the checkout")
    return commit


def read_current(directory):
    path = directory / "current.sh"
    try:
        metadata = aws("get-object", "--key", LATEST_KEY, str(path))
    except subprocess.CalledProcessError as error:
        # Access denial, missing buckets and outages are NOT first releases.
        if "(NoSuchKey)" in (error.stderr or ""):
            return None, None
        raise
    if not metadata.get("ETag"):
        raise ValueError("R2 current installer has no ETag")
    return path.read_bytes(), metadata


def select_version(root, source, commit, current):
    requested = field(source, "INSTALLER_VERSION")
    if requested != DEVELOPMENT_VERSION:
        version_tuple(requested)
    if field(source, "INSTALLER_SOURCE_COMMIT") != "development":
        raise ValueError("Source commit marker must remain development in Git")
    if current is None:
        return "0.1.0" if requested == DEVELOPMENT_VERSION else requested

    previous_version = field(current, "INSTALLER_VERSION")
    previous_commit = field(current, "INSTALLER_SOURCE_COMMIT")
    version_tuple(previous_version)
    if not COMMIT.fullmatch(previous_commit):
        raise ValueError("Invalid published source commit")
    previous_source = git(root, "show", f"{previous_commit}:static/install.sh")
    if render(previous_source, previous_version, previous_commit) != current:
        raise ValueError("Published installer does not match its Git source")
    # Reject an old checkout even if it would otherwise allocate a higher version.
    git(root, "merge-base", "--is-ancestor", previous_commit, commit)
    if previous_commit == commit:
        return previous_version
    if requested != DEVELOPMENT_VERSION and requested != field(previous_source, "INSTALLER_VERSION"):
        if version_tuple(requested) <= version_tuple(previous_version):
            raise ValueError("Explicit installer version must exceed the published version")
        return requested
    major, minor, patch = version_tuple(previous_version)
    return f"{major}.{minor}.{patch + 1}"


def verify(expected, directory):
    for attempt in range(ATTEMPTS):
        try:
            downloaded = directory / "verified.sh"
            metadata = aws("get-object", "--key", LATEST_KEY, str(downloaded))
            if (downloaded.read_bytes() != expected
                    or metadata.get("ContentType") != CONTENT_TYPE
                    or metadata.get("CacheControl") != "no-store"):
                raise RuntimeError("R2 content or metadata mismatch")
            # Verify the actual public URL, not a cache-busting query.
            with urlopen(f"{CDN}/{LATEST_KEY}", timeout=15) as response:
                if response.read(len(expected) + 1) != expected:
                    raise RuntimeError("CDN content mismatch")
                directives = {s.strip().lower() for s in response.headers.get("Cache-Control", "").split(",")}
                if "no-store" not in directives:
                    raise RuntimeError("CDN installer must return Cache-Control: no-store")
            return
        except (OSError, RuntimeError, subprocess.SubprocessError) as error:
            if attempt == ATTEMPTS - 1:
                raise RuntimeError(f"Installer verification failed: {error}") from error
            time.sleep(5)


def publish(source, report=None):
    for name in ("R2_ENDPOINT", "R2_BUCKET", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"):
        if not os.environ.get(name):
            raise ValueError(f"{name} is required")
    original = source.read_bytes()
    if not original.startswith(b"#!/bin/sh\n"):
        raise ValueError("Expected a nonempty POSIX shell installer")
    subprocess.run(["sh", "-n", str(source)], check=True, timeout=15)
    root = source.parent.parent
    commit = source_identity(root, original)
    with tempfile.TemporaryDirectory(prefix="eigenflux-installer-") as temp:
        directory = Path(temp)
        current, metadata = read_current(directory)
        version = select_version(root, original, commit, current)
        expected = render(original, version, commit)
        digest = hashlib.sha256(expected).hexdigest()
        record = {"version": version, "source_commit": commit, "sha256": digest,
                  "url": f"{CDN}/{LATEST_KEY}", "verified": False}
        if report:
            report.parent.mkdir(parents=True, exist_ok=True)
            report.write_text(json.dumps(record, indent=2) + "\n")
        if (metadata is None or expected != current
                or metadata.get("ContentType") != CONTENT_TYPE
                or metadata.get("CacheControl") != "no-store"):
            snapshot = directory / "install.sh"
            snapshot.write_bytes(expected)
            # Only replace the exact object we read. A concurrent writer fails
            # this run instead of silently overwriting another release.
            condition = ("--if-match", metadata["ETag"]) if metadata else ("--if-none-match", "*")
            aws("put-object", "--key", LATEST_KEY, "--body", str(snapshot),
                "--content-type", CONTENT_TYPE, "--cache-control", "no-store", *condition)
        verify(expected, directory)
        record["verified"] = True
        if report:
            report.write_text(json.dumps(record, indent=2) + "\n")
    message = f"Installer {version}\nSource: {commit}\nSHA-256: {digest}\nVerified: {CDN}/{LATEST_KEY}\n"
    print(message)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
            summary.write(message)
    return record


if __name__ == "__main__":
    root = Path(__file__).resolve().parents[2]
    try:
        publish(root / "static/install.sh", root / "build/installer-release.json")
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"Installer publish failed: {error}", file=sys.stderr)
        sys.exit(1)
