"""Verify the publicly hosted CLI and Skills in disposable native state."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
from urllib.request import urlopen


def read_public(base, path):
    with urlopen(base + "/" + path, timeout=35) as response:
        return response.read()


def check_native(provenance, hashes):
    base = provenance["base_url"]
    version = provenance["cli_version"]
    relative = f"cli/{version}/eigenflux-darwin-arm64"
    expected_commit = provenance["source_commit"][:12] + "+snapshot." + provenance["distribution_commit"][:12]
    manifest = json.loads(read_public(base, "skills/latest/manifest.json"))
    if manifest["revision"] != provenance["skills_revision"]:
        raise ValueError("wrong remote Skills revision")
    env = {k: v for k, v in os.environ.items() if k in {"HOME", "USER", "TMPDIR", "PATH", "LANG"}}
    env.update(EIGENFLUX_SKIP_AGENT_SETUP="1", EIGENFLUX_CDN_URL="https://example.invalid")
    with tempfile.TemporaryDirectory(prefix="eigenflux-snapshot-verification-") as temp:
        root = Path(temp).resolve()
        download, stable = root / "download", root / "bin space" / "eigenflux"
        agent_home, skills_dir = root / "nondefault agent" / ".eigenflux", root / "host skills"
        data = read_public(base, relative)
        if hashlib.sha256(data).hexdigest() != hashes[relative]:
            raise ValueError("bootstrap binary checksum mismatch")
        download.write_bytes(data)
        download.chmod(0o755)

        def cli(binary, *args, check=True):
            return subprocess.run([str(binary), "--homedir", str(agent_home), *args], env=env, text=True,
                                  capture_output=True, check=check, timeout=90)

        cli(download, "installation", "replace", "--destination", str(stable), "--host", "codex", "--skills-target", str(skills_dir))
        cli(stable, "update", "--format", "json")
        active = Path(str(stable) + ".versions") / version / "eigenflux"
        if not active.is_file() or hashlib.sha256(active.read_bytes()).hexdigest() != hashes[relative]:
            raise ValueError("activated CLI differs from signed bootstrap")
        if not (active.parent / "EigenFlux Notifications.app/Contents/MacOS/EigenFluxNotifications").is_file():
            raise ValueError("paired macOS notification app was not installed")
        info = json.loads(cli(stable, "version", "--format", "json").stdout)
        if (info.get("commit"), info.get("home")) != (expected_commit, str(agent_home)):
            raise ValueError("wrong CLI source or non-default Agent Home")
        skills_dir.mkdir(exist_ok=True)
        unrelated = skills_dir / "personal-skill"
        unrelated.mkdir()
        (unrelated / "SKILL.md").write_text("unmanaged\n")
        first = json.loads(cli(stable, "skills", "sync", "--host", "codex", "--format", "json").stdout)
        second = json.loads(cli(stable, "skills", "sync", "--if-stale", "--quiet", "--host", "codex", "--format", "json").stdout)
        local = json.loads((skills_dir / ".ef-manifest.json").read_text())
        if not (first["atomic"] and first["verified_manifest"] and second["verified_manifest"] and
                not second.get("atomic", False) and second["source"] == "local" and
                local["signature"] == manifest["signature"] and (unrelated / "SKILL.md").read_text() == "unmanaged\n"):
            raise ValueError("native sync did not retain the exact revision and unrelated files")
        prefill = skills_dir / "ef-onboarding/references/prefill.md"
        original = prefill.read_bytes()
        prefill.write_bytes(original + b"\nLocal edit\n")
        failed = cli(stable, "skills", "sync", "--host", "codex", "--format", "json", check=False)
        if failed.returncode == 0 or prefill.read_bytes() != original + b"\nLocal edit\n":
            raise ValueError("drift was silently accepted or a local edit was overwritten")
    print(f"Verified native CLI package, fixed origin and repeated Skills sync: {manifest['revision']}", flush=True)
