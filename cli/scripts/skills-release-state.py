#!/usr/bin/env python3
"""Allocate and reserve signed Skills releases against R2, never CDN cache."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import shutil
import tempfile

LATEST = ("skills/latest", "cli/latest")
ARCHIVE = "skills/releases/"


def aws(*args):
    try:
        result = subprocess.run(
            ["aws", "--endpoint-url", os.environ["R2_ENDPOINT"], "--region", "auto", "s3api", *args,
             "--bucket", os.environ["R2_BUCKET"], "--output", "json"],
            check=True, capture_output=True, text=True,
        )
    except subprocess.CalledProcessError as error:
        raise RuntimeError(f"R2 {args[0]} failed: {error.stderr.strip()}") from error
    return json.loads(result.stdout or "{}")


def verified(path, checker):
    subprocess.run([str(checker), str(path)], check=True, stdout=subprocess.DEVNULL)
    return json.loads(path.read_text())


def archive_head():
    # AWS CLI pagination is enabled: failed/partial releases also consume a number.
    listing = aws("list-objects-v2", "--prefix", ARCHIVE)
    numbers = []
    for item in listing.get("Contents", []):
        match = re.fullmatch(r"skills/releases/([1-9][0-9]*)/manifest.json", item["Key"])
        if match:
            numbers.append(int(match.group(1)))
    return max(numbers, default=0)


def snapshot(state, checker):
    records = []
    for index, prefix in enumerate(LATEST):
        path = state / f"previous-{index}.json"
        aws("get-object", "--key", f"{prefix}/manifest.json", str(path))
        records.append(verified(path, checker))
    head = archive_head()
    if head:
        path = state / "archive-head.json"
        aws("get-object", "--key", f"{ARCHIVE}{head}/manifest.json", str(path))
        record = verified(path, checker)
        if record["sequence"] != head:
            raise RuntimeError("archive key and signed sequence disagree")
        records.append(record)
    return records


def next_sequence(records):
    highest = max(record["sequence"] for record in records)
    if highest >= 2**64 - 1:
        raise RuntimeError("Skills sequence exhausted")
    return highest + 1


def require_transition(candidate, records, planned):
    if candidate["sequence"] != planned or planned <= max(r["sequence"] for r in records):
        raise RuntimeError("release sequence is stale or already used; start a new workflow run")


def put_new(path, key):
    # R2 enforces uniqueness atomically. A collision fails before latest is touched.
    try:
        aws("put-object", "--key", key, "--body", str(path), "--if-none-match", "*")
    except RuntimeError as error:
        if "PreconditionFailed" not in str(error) and "ConditionalRequestConflict" not in str(error):
            raise
        with tempfile.TemporaryDirectory() as directory:
            previous = Path(directory) / "object"
            aws("get-object", "--key", key, str(previous))
            if previous.read_bytes() != path.read_bytes():
                raise RuntimeError("immutable Skills object differs: " + key) from error


def prepare(state, checker):
    state.mkdir(parents=True, exist_ok=True)
    records = snapshot(state, checker)
    sequence = next_sequence(records)
    (state / "plan.json").write_text(json.dumps({"sequence": sequence, "records": records}))
    print(sequence)


def same_release(candidate, previous):
    required = {"revision", "tar_sha256", "key_id", "skills", "managed_by"}
    if not required.issubset(candidate) or not required.issubset(previous):
        return False
    # cli_version is the informational producer tag, not compatibility.
    ignored = {"sequence", "signature", "generated_at", "cli_version"}
    return ({k: v for k, v in candidate.items() if k not in ignored}
            == {k: v for k, v in previous.items() if k not in ignored})


def check_archive(build, candidate):
    digest = hashlib.sha256((build / "skills.tar.gz").read_bytes()).hexdigest()
    if digest != candidate["tar_sha256"] or digest != (build / "skills.tar.gz.sha256").read_text().strip():
        raise RuntimeError("built archive does not match signed manifest and sidecar")
    return digest


def verify_origin_payloads(state, expected):
    for index, prefix in enumerate(LATEST):
        archive = state / f"unchanged-{index}.tar.gz"
        sidecar = state / f"unchanged-{index}.sha256"
        aws("get-object", "--key", f"{prefix}/skills.tar.gz", str(archive))
        aws("get-object", "--key", f"{prefix}/skills.tar.gz.sha256", str(sidecar))
        if (hashlib.sha256(archive.read_bytes()).hexdigest() != expected["tar_sha256"]
                or sidecar.read_text().strip() != expected["tar_sha256"]):
            raise RuntimeError("unchanged Skills origin payload is missing or differs")


def reserve(state, checker, build):
    plan = json.loads((state / "plan.json").read_text())
    candidate = verified(build / "manifest.json", checker)
    # The shared publication lock is held here, never during preparation/tests.
    current = snapshot(state, checker)
    if len(current) >= 2 and current[0] == current[1] and same_release(candidate, current[0]):
        check_archive(build, candidate)
        verify_origin_payloads(state, current[0])
        shutil.copyfile(state / "previous-0.json", build / "manifest.json")
        (state / "publication.json").write_text(json.dumps({"action": "noop", "sequence": current[0]["sequence"]}))
        print(f'unchanged signed Skills sequence {current[0]["sequence"]}; publication skipped', flush=True)
        return
    # Resume only the exact prepared signature, never a regenerated candidate.
    resuming = (candidate["sequence"] == plan["sequence"] and len(current) >= 3
                and current[-1] == candidate
                and candidate["sequence"] == max(r["sequence"] for r in current)
                and all(r == candidate or r in plan["records"] for r in current))
    if resuming and (state / "archive-head.json").read_bytes() != (build / "manifest.json").read_bytes():
        raise RuntimeError("reserved signed manifest bytes differ from prepared artifact")
    if not resuming:
        require_transition(candidate, current, plan["sequence"])
        if current != plan["records"]:
            raise RuntimeError("R2 release state changed during build; refusing to publish")
    check_archive(build, candidate)
    prefix = f'{ARCHIVE}{candidate["sequence"]}'
    # Existing immutable bytes must match on a job retry; partial uploads resume.
    put_new(build / "manifest.json", f"{prefix}/manifest.json")
    for name in ("skills.tar.gz", "skills.tar.gz.sha256"):
        put_new(build / name, f"{prefix}/{name}")
    (state / "publication.json").write_text(json.dumps({"action": "publish", "sequence": candidate["sequence"]}))
    print(f'reserved signed sequence {candidate["sequence"]}', flush=True)


def verify_live(state, checker, build):
    expected = verified(build / "manifest.json", checker)
    for index, prefix in enumerate(LATEST):
        path = state / f"live-{index}.json"
        aws("get-object", "--key", f"{prefix}/manifest.json", str(path))
        if verified(path, checker) != expected:
            raise RuntimeError(f"{prefix} differs from the reserved signed release")
    print(f'verified origin sequence {expected["sequence"]}', flush=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("prepare", "reserve", "verify"))
    parser.add_argument("--build", type=Path, required=True)
    args = parser.parse_args()
    state = args.build / "release-state"
    checker = args.build / "manifestcheck"
    if args.action == "prepare":
        prepare(state, checker)
    elif args.action == "reserve":
        reserve(state, checker, args.build)
    else:
        verify_live(state, checker, args.build)


if __name__ == "__main__":
    main()
