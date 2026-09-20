#!/usr/bin/env python3
"""Build an immutable CLI/Skills distribution without injecting test instructions."""
import argparse
import base64
import difflib
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile

SOURCE = "47afec0bf403b320df40b3a8ed3ba9ef49842b44"


def run(args, **kw):
    return subprocess.run([str(a) for a in args], check=True, text=True, capture_output=True, **kw).stdout.strip()


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def replace_once(text, before, after):
    if text.count(before) != 1:
        raise ValueError("Source contract changed: " + before[:100])
    return text.replace(before, after)


def snapshot_prefix(harness):
    if not re.fullmatch(r"[0-9a-f]{40}", harness):
        raise ValueError("Harness revision must be a full Git commit")
    return "snapshots/" + SOURCE + "/" + harness


def installer(source, base):
    text = replace_once(source, 'CDN_URL="${EIGENFLUX_CDN_URL:-https://cdn.eigenflux.ai}"', 'CDN_URL="' + base + '"')
    # Equal semantic versions do not identify a snapshot binary. Respect the
    # actual install destination rather than another executable on PATH.
    text = replace_once(text, '''    if [ "$CURRENT_VERSION" = "$LATEST_VERSION" ]; then
      ok "eigenflux ${CURRENT_VERSION} is already up to date."
      return
    fi''', '''    if [ "$CURRENT_VERSION" = "$LATEST_VERSION" ]; then
      EXPECTED_SHA=$(curl -fsSL "${CDN_URL}/cli/${LATEST_VERSION}/${BIN_NAME}.sha256")
      INSTALLED_BIN="${EIGENFLUX_INSTALL_DIR:-$HOME/.local/bin}/eigenflux"
      if [ -f "$INSTALLED_BIN" ] && [ "$(snapshot_sha256 "$INSTALLED_BIN")" = "$EXPECTED_SHA" ]; then
        ok "eigenflux ${CURRENT_VERSION} is already up to date."
        return
      fi
    fi''')
    text = replace_once(text, '  chmod +x "$TMP_FILE"', '''  EXPECTED_SHA=$(curl -fsSL "${DOWNLOAD_URL}.sha256")
  if [ "$(snapshot_sha256 "$TMP_FILE")" != "$EXPECTED_SHA" ]; then
    rm -f "$TMP_FILE"
    err "Downloaded CLI checksum mismatch"
    exit 1
  fi
  chmod +x "$TMP_FILE"''')
    start = text.index('  info "R2 unreachable — bootstrapping skills from GitHub')
    end = text.index('\n}\n', start)
    text = text[:start] + '  err "Signed Skills synchronization failed; installation is incomplete"\n  return 1' + text[end:]
    text = text.replace('GITHUB_REPO="phronesis-io/eigenflux"\nBRANCH="main"\n', "")
    helper = '''
snapshot_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

verify_snapshot_cli() {
  ACTIVE_BIN=$(command -v eigenflux 2>/dev/null || true)
  EXPECTED_SHA=$(curl -fsSL "${CDN_URL}/cli/${LATEST_VERSION}/${BIN_NAME}.sha256")
  if [ ! -f "$ACTIVE_BIN" ] || [ "$(snapshot_sha256 "$ACTIVE_BIN")" != "$EXPECTED_SHA" ]; then
    err "The CLI selected by PATH does not match the installed distribution"
    return 1
  fi
}
'''
    text = replace_once(text, '# ── Step 2: Install skills', helper + '\n# ── Step 2: Install skills')
    text = replace_once(text, '\ninstall_cli\nreport_attribution', '\ninstall_cli\nverify_snapshot_cli\nreport_attribution')
    text = replace_once(text, '\nsetup_agents\n', '\nsetup_agents\nverify_snapshot_cli\ninstall_skills\n')
    return text.replace('https://www.eigenflux.ai/install.sh', base + '/install.sh')


def make(build_dir, base, platforms):
    repo = Path(__file__).resolve().parents[2]
    harness = run(["git", "-C", repo, "rev-parse", "HEAD"])
    prefix = snapshot_prefix(harness)
    if not base.startswith("https://") and not re.fullmatch(r"http://127\.0\.0\.1:\d+", base):
        raise ValueError("Expected HTTPS publication origin or a loopback fixture")
    base = base.rstrip("/") + "/" + prefix
    if build_dir.exists():
        raise ValueError("Output directory exists; do not reuse an immutable build")
    build_dir.mkdir(parents=True)
    source = build_dir / "source"
    source.mkdir()
    archive = subprocess.run(["git", "-C", repo, "archive", SOURCE, "cli", "skills", "static"], check=True, capture_output=True).stdout
    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
        for member in tar.getmembers():
            if member.issym() or member.islnk() or member.name.startswith("/") or ".." in Path(member.name).parts:
                raise ValueError("Unsafe archive member")
        tar.extractall(source)
    cli = source / "cli"
    raw_config = (cli / ".cli.config").read_text()
    version = re.search(r"^CLI_VERSION=(\S+)$", raw_config, re.M)[1]
    minimum = re.search(r"^SKILLS_MIN_CLI_VERSION=(\S+)$", raw_config, re.M)[1]
    public = build_dir / "public"
    public.mkdir()
    audit = build_dir / "audit"
    audit.mkdir()
    stage = build_dir / "skills"
    stage.mkdir()
    names = run(["go", "run", "./cmd/manifestgen", "--print-allowlist", "--skills-dir", source / "skills"], cwd=cli).splitlines()
    for name in names:
        shutil.copytree(source / "skills" / name, stage / name)
    # Keep resume/repair in the same installation channel without adding prose.
    onboarding = stage / "ef-onboarding/SKILL.md"
    original = onboarding.read_text()
    amended = replace_once(original, "https://cdn.eigenflux.ai/skills/latest/install.md", base + "/install.md")
    amended = replace_once(amended, 'version: "0.2.7"', 'version: "0.2.8"')
    onboarding.write_text(amended)
    (audit / "skills.patch").write_text("".join(difflib.unified_diff(original.splitlines(True), amended.splitlines(True), fromfile="source/ef-onboarding/SKILL.md", tofile="snapshot/ef-onboarding/SKILL.md")))
    archive_path = public / "skills/latest/skills.tar.gz"
    archive_path.parent.mkdir(parents=True)
    with archive_path.open("wb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", mtime=0, filename="") as gz, tarfile.open(fileobj=gz, mode="w") as tar:
        for path in sorted(stage.rglob("*")):
            info = tar.gettarinfo(str(path), arcname=str(path.relative_to(stage)))
            info.uid = info.gid = info.mtime = 0
            info.uname = info.gname = ""
            if path.is_file():
                with path.open("rb") as content:
                    tar.addfile(info, content)
            else:
                tar.addfile(info)
    private = build_dir / "signing-seed"
    private.write_text(base64.b64encode(os.urandom(32)).decode())
    private.chmod(0o600)
    try:
        pubkey = run(["go", "run", "./cmd/manifestgen", "--print-public-key", "--signing-key-file", private], cwd=cli)
        manifest_path = archive_path.parent / "manifest.json"
        run(["go", "run", "./cmd/manifestgen", "--skills-dir", stage, "--cli-version", version,
             "--min-cli-version", minimum, "--sequence", "1", "--signing-key-file", private,
             "--tarball", archive_path, "--out", manifest_path], cwd=cli)
    finally:
        private.unlink(missing_ok=True)
    manifest = json.loads(manifest_path.read_text())
    revision = manifest["revision"]
    overlay = build_dir / "overlay"
    overlay.mkdir()
    replacements = {}
    def patch(relative, changed):
        original_path = cli / relative
        path = overlay / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(changed)
        replacements[str(original_path)] = str(path)
        (audit / (relative.replace("/", "_") + ".patch")).write_text("".join(difflib.unified_diff(original_path.read_text().splitlines(True), changed.splitlines(True), fromfile=relative, tofile="snapshot/" + relative)))
    original = (cli / "cmd/skills.go").read_text()
    body = '''func cdnBase() string {
	if v := os.Getenv("EIGENFLUX_CDN_URL"); v != "" {
		return v
	}
	return skills.CDNDefault
}'''
    patch("cmd/skills.go", replace_once(original, body, 'func cdnBase() string {\n\treturn ' + json.dumps(base) + '\n}').replace('\n\t"os"', ""))
    original = (cli / "internal/skills/sync.go").read_text()
    postcondition = '''func Sync(opts SyncOptions) (result *SyncResult, syncErr error) {
    defer func() {
        if syncErr != nil || result == nil { return }
        installed, err := ReadLocalManifest(result.SkillsDir)
        if err != nil || installed == nil || installed.Revision != "__REVISION__" {
            result = nil
            syncErr = fmt.Errorf("installed Skills do not match this CLI distribution")
            return
        }
        if err := verifyManifestSignature(installed); err != nil {
            result = nil; syncErr = err; return
        }
        if err := verifyInstalledSkills(result.SkillsDir, installed); err != nil {
            result = nil; syncErr = err
        }
    }()
'''.replace("__REVISION__", revision)
    patch("internal/skills/sync.go", replace_once(original, "func Sync(opts SyncOptions) (*SyncResult, error) {", postcondition))
    patch("cmd/doctor.go", (cli / "cmd/doctor.go").read_text().replace("https://www.eigenflux.ai/install.sh", base + "/install.sh"))
    overlay_file = overlay / "overlay.json"
    overlay_file.write_text(json.dumps({"Replace": replacements}, indent=2))
    bindir = public / "cli" / version
    bindir.mkdir(parents=True)
    for platform in platforms:
        target_os, arch = platform.split("/")
        if target_os not in ("darwin", "linux") or arch not in ("arm64", "amd64"):
            raise ValueError("Unsupported platform " + platform)
        binary = bindir / ("eigenflux-" + target_os + "-" + arch)
        run(["go", "build", "-overlay", overlay_file, "-ldflags",
             f"-X main.Version={version} -X main.Commit={SOURCE}+snapshot.{harness[:12]} -X cli.eigenflux.ai/internal/skills.VerifyPublicKeyBase64={pubkey}",
             "-o", binary, "."], cwd=cli, env=dict(os.environ, GOOS=target_os, GOARCH=arch, CGO_ENABLED="0"))
        binary.with_suffix(".sha256").write_text(digest(binary) + "\n")
    latest = public / "cli/latest"
    latest.mkdir(parents=True)
    (latest / "version.txt").write_text(version + "\n")
    for name in ["manifest.json", "skills.tar.gz"]:
        shutil.copy2(archive_path.parent / name, latest / name)
    original_installer = (source / "static/install.sh").read_text()
    actual_installer = installer(original_installer, base)
    (public / "install.sh").write_text(actual_installer)
    run(["sh", "-n", public / "install.sh"])
    (audit / "installer.patch").write_text("".join(difflib.unified_diff(original_installer.splitlines(True), actual_installer.splitlines(True), fromfile="static/install.sh", tofile="snapshot/install.sh")))
    (public / "install.ps1").write_text('throw "This distribution supports macOS and Linux only."\n')
    entry = (source / "skills/install.md").read_text().replace("https://www.eigenflux.ai/install.sh", base + "/install.sh").replace("https://eigenflux.ai/install.ps1", base + "/install.ps1")
    (public / "install.md").write_text(entry)
    # Fail if staging accidentally changes a business Skill or adds prompt text.
    changed = []
    for path in stage.rglob("*"):
        if path.is_file() and path.read_bytes() != (source / "skills" / path.relative_to(stage)).read_bytes():
            changed.append(str(path.relative_to(stage)))
    if changed != ["ef-onboarding/SKILL.md"]:
        raise ValueError("Unexpected Skill modifications: " + repr(changed))
    provenance = {"source_commit": SOURCE, "distribution_commit": harness, "prefix": prefix, "base_url": base,
                  "cli_version": version, "skills_revision": revision, "skills_public_key": pubkey,
                  "platforms": platforms, "cli_overlays": sorted(str(Path(p).relative_to(cli)) for p in replacements),
                  "skill_changes": changed, "entry_change": "installer URLs only", "scaffolding": "scripts/snapshots/CONTRACT.md"}
    (public / "provenance.json").write_text(json.dumps(provenance, indent=2) + "\n")
    checksums = {str(p.relative_to(public)): digest(p) for p in sorted(public.rglob("*")) if p.is_file()}
    (public / "checksums.json").write_text(json.dumps(checksums, indent=2) + "\n")
    print(json.dumps(provenance, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--origin", default="https://cdn.eigenflux.ai")
    parser.add_argument("--platform", action="append", dest="platforms")
    args = parser.parse_args()
    make(args.out.resolve(), args.origin, args.platforms or ["darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64"])
