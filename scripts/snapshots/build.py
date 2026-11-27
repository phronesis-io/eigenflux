#!/usr/bin/env python3
"""Build a pinned macOS arm64 onboarding release using the existing paired test packager."""
import argparse
import difflib
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile

PRODUCT = "a17183f4e0f2ae4eb268e646a204afdea4b11f88"
ORIGIN = "https://cdn.eigenflux.ai"
ROOT = Path(__file__).resolve().parents[2]


def run(*argv, cwd=ROOT):
    return subprocess.check_output(argv, cwd=cwd, text=True).strip()


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def replace(text, old, new):
    if text.count(old) != 1:
        raise ValueError(f"source contract changed: {old[:110]}")
    return text.replace(old, new)


def write_overlay(source, audit, relative, transform):
    path = source / relative
    old = path.read_text()
    new = transform(old)
    if old == new:
        raise ValueError(f"missing overlay change: {relative}")
    path.write_text(new)
    (audit / (relative.replace("/", "_") + ".patch")).write_text(
        "".join(difflib.unified_diff(
            old.splitlines(True), new.splitlines(True), fromfile=relative, tofile="snapshot/" + relative
        ))
    )


def fixed_origin(text, base):
    return replace(text, """func cdnBase() string {
	if v := os.Getenv("EIGENFLUX_CDN_URL"); v != "" {
		return v
	}
	return skills.CDNDefault
}""", "func cdnBase() string {\n\treturn " + json.dumps(base) + "\n}")


def sync_postcondition(text, base):
    text = replace(text, "func Sync(opts SyncOptions) (result *SyncResult, err error) {", """var snapshotSkillsRevision string

func Sync(opts SyncOptions) (result *SyncResult, err error) {
	if snapshotSkillsRevision != "" {
		defer func() {
			if err != nil {
				return
			}
			if result == nil || !result.VerifiedManifest {
				err = fmt.Errorf("snapshot Skills synchronization was not verified")
				return
			}
			m, verifyErr := ReadLocalManifest(result.SkillsDir)
			if verifyErr != nil || m == nil || m.Revision != snapshotSkillsRevision {
				err = fmt.Errorf("installed Skills do not match the snapshot revision")
				return
			}
			if err = ValidateSignedRelease(m); err == nil {
				err = verifyInstalledSkills(result.SkillsDir, m)
			}
		}()
	}
""".rstrip())
    return replace(text, "https://cdn.eigenflux.ai/skills/latest/install.md", base + "/install.md")


def stage_packager(text, build_commit):
    start = text.index('\troot, err := repositoryRoot()')
    end = text.index('\tconfig, err := os.ReadFile', start)
    text = text[:start] + """	root, err := filepath.Abs("..")
	if err != nil {
		return err
	}
""" + text[end:]
    start = text.index('\tcommitBytes, err := exec.Command("git"')
    end = text.index('\tvar private ed25519.PrivateKey', start)
    text = text[:start] + "\tcommit := " + json.dumps(build_commit) + "\n" + text[end:]
    package_skills = """		if err := packageSkills(root, *out, version, minimum, private); err != nil {
			return err
		}
"""
    text = replace(text, package_skills, "")
    anchor = '\tif err := build(root, *target, binary, ".", ldflags); err != nil {'
    text = replace(text, anchor, """	if !*testBundle {
		return fmt.Errorf("snapshot requires the paired test packager")
	}
	if err := packageSkills(root, *out, version, minimum, private); err != nil {
		return err
	}
	signedData, err := os.ReadFile(filepath.Join(*out, "cdn", "skills", "latest", skills.RemoteManifest))
	if err != nil {
		return err
	}
	var signed skills.Manifest
	if err := json.Unmarshal(signedData, &signed); err != nil {
		return err
	}
	ldflags += " -X cli.eigenflux.ai/internal/skills.snapshotSkillsRevision=" + signed.Revision
""" + anchor)
    text = replace(text, "m.Sequence = uint64(time.Now().UnixNano())", "m.Sequence = 1")
    return text


def installer(text, base, key_id):
    text = replace(text, 'CDN_URL="${EIGENFLUX_CDN_URL:-https://cdn.eigenflux.ai}"', f'CDN_URL="{base}"')
    text = replace(text, 'INSTALLER_SOURCE_COMMIT="development"', f'INSTALLER_SOURCE_COMMIT="{PRODUCT}"')
    text = replace(text, '# ── Step 1: Install CLI binary', f'''snapshot_preflight_skills() {{
  snapshot_target=$("$1" --homedir "$2" skills path --host "$INVOKING_HOST") || return 1
  [ -n "$snapshot_target" ] || return 1
  if [ -e "$snapshot_target/.ef-manifest.json" ] || [ -L "$snapshot_target/.ef-manifest.json" ]; then
    if [ -L "$snapshot_target/.ef-manifest.json" ] || ! grep -Fq '"key_id": "{key_id}"' "$snapshot_target/.ef-manifest.json"; then
      err "The Skills directory already belongs to another distribution. Use a clean test environment."
      return 1
    fi
  fi
}}

# ── Step 1: Install CLI binary''')
    text = replace(text, '  info "Detected: ${OS}/${ARCH}"', '''  if [ "$OS/$ARCH" != darwin/arm64 ]; then
    err "This installation distribution supports macOS on Apple silicon."
    return 1
  fi
  info "Detected: ${OS}/${ARCH}"''')
    start = text.index('  CURRENT_VERSION=""\n')
    end = text.index('  TMP_FILE=$(mktemp)', start)
    text = text[:start] + '''  DOWNLOAD_URL="${CDN_URL}/cli/${LATEST_VERSION}/${BIN_NAME}"
  INSTALL_DIR="${EIGENFLUX_INSTALL_DIR:-$HOME/.local/bin}"
  INSTALL_HOME=$(resolve_eigenflux_home "$HOMEDIR_FLAG" "$EXPLICIT_EIGENFLUX_HOME" "$INVOKING_HOST")
  SNAPSHOT_EXPECTED_SHA=$(curl -fsSL "${DOWNLOAD_URL}.sha256") || return 1
  case "$SNAPSHOT_EXPECTED_SHA" in
    *[!a-f0-9]*|'') err "Invalid CLI checksum"; return 1 ;;
  esac
  [ "${#SNAPSHOT_EXPECTED_SHA}" -eq 64 ] || return 1
  snapshot_path_cli=$(command -v eigenflux 2>/dev/null || true)
  for snapshot_candidate in "$snapshot_path_cli" "$INSTALL_DIR/eigenflux"; do
    [ -n "$snapshot_candidate" ] || continue
    if [ -e "$snapshot_candidate" ] || [ -L "$snapshot_candidate" ]; then
      if [ -L "$snapshot_candidate" ] || [ ! -f "$snapshot_candidate" ] ||
         [ "$(shasum -a 256 "$snapshot_candidate" | awk '{print $1}')" != "$SNAPSHOT_EXPECTED_SHA" ]; then
        err "An existing EigenFlux CLI belongs to another distribution. Use a clean test environment."
        return 1
      fi
    fi
  done
  if [ -n "$snapshot_path_cli" ]; then
    INSTALLED_CLI_PATH="$snapshot_path_cli"
    snapshot_preflight_skills "$INSTALLED_CLI_PATH" "$INSTALL_HOME" || return 1
    complete_macos_install
    ok "eigenflux ${LATEST_VERSION} is already up to date."
    return
  fi

''' + text[end:]
    text = replace(text, '  "$TMP_FILE" --homedir "$INSTALL_HOME" installation replace', '''  if ! snapshot_preflight_skills "$TMP_FILE" "$INSTALL_HOME"; then
    rm -f "$TMP_FILE"
    return 1
  fi
  "$TMP_FILE" --homedir "$INSTALL_HOME" installation replace''')
    start = text.index('  info "Skills sync failed — checking GitHub bootstrap compatibility"')
    end = text.index('\n}\n', start)
    text = text[:start] + '  err "Signed Skills synchronization failed; installation is incomplete"\n  return 1' + text[end:]
    text = replace(text, 'setup_agents\n\nif [ "${EIGENFLUX_SKIP_AGENT_SETUP:-}" != "1" ]; then', '''setup_agents
SNAPSHOT_ACTIVE_CLI=$(command -v eigenflux 2>/dev/null || true)
if [ -z "$SNAPSHOT_ACTIVE_CLI" ] ||
   [ "$(shasum -a 256 "$SNAPSHOT_ACTIVE_CLI" | awk '{print $1}')" != "$SNAPSHOT_EXPECTED_SHA" ]; then
  err "The CLI on PATH does not match this distribution"
  exit 1
fi
install_skills

if [ "${EIGENFLUX_SKIP_AGENT_SETUP:-}" != "1" ]; then''')
    text = replace(text, 'GITHUB_REPO="phronesis-io/eigenflux"\nBRANCH="main"\n', "")
    return text.replace(ORIGIN + "/skills/latest/install.md", base + "/install.md").replace(
        "https://www.eigenflux.ai/install.sh", base + "/install.sh"
    )


def main():
    args = argparse.ArgumentParser()
    args.add_argument("--out", type=Path, required=True)
    args = args.parse_args()
    output = args.out.resolve()
    harness = run("git", "rev-parse", "HEAD")
    if not re.fullmatch(r"[0-9a-f]{40}", harness):
        raise ValueError("distribution requires a full commit SHA")
    run("git", "merge-base", "--is-ancestor", PRODUCT, harness)
    if run("uname", "-sm") != "Darwin arm64":
        raise ValueError("snapshot requires a native macOS arm64 builder")
    prefix = f"snapshots/{PRODUCT}/{harness}"
    base = f"{ORIGIN}/{prefix}"
    public, audit = output / "public", output / "audit"
    output.mkdir(parents=True)
    audit.mkdir()
    with tempfile.TemporaryDirectory(dir=output, prefix="stage-") as tmp:
        tmp = Path(tmp)
        source = tmp / "source"
        source.mkdir()
        archive = tmp / "product.tar"
        subprocess.run(["git", "archive", "--output", str(archive), PRODUCT, "cli", "skills", "static/install.sh"],
                       cwd=ROOT, check=True)
        with tarfile.open(archive) as tar:
            tar.extractall(source, filter="data")
        amend = lambda path, fn: write_overlay(source, audit, path, fn)
        amend("skills/ef-onboarding/SKILL.md", lambda s: replace(s, ORIGIN + "/skills/latest/install.md", base + "/install.md"))
        amend("cli/cmd/skills.go", lambda s: fixed_origin(s, base))
        amend("cli/cmd/doctor.go", lambda s: replace(s, "https://www.eigenflux.ai/install.sh", base + "/install.sh"))
        amend("cli/internal/skills/sync.go", lambda s: sync_postcondition(s, base))
        amend("cli/cmd/devbuild/main.go", lambda s: stage_packager(s, PRODUCT[:12] + "+snapshot." + harness[:12]))
        paired = tmp / "paired"
        print(run("go", "run", "./cmd/devbuild", "-test-bundle", "-target", "darwin/arm64", "-out", str(paired), cwd=source / "cli"))
        shutil.copytree(paired / "cdn", public)
        info = json.loads((paired / "build-info.json").read_text())
        version = info["version"]
        binpath = public / "cli" / version / "eigenflux-darwin-arm64"
        assert digest(binpath) == info["sha256"]
        sidecar = binpath.with_suffix(".sha256")
        sidecar.write_text(info["sha256"] + "\n")
        (public / "cli/latest/version.txt").write_text(version + "\n")
        signed = public / "skills/latest"
        for path in signed.iterdir():
            shutil.copy2(path, public / "cli/latest" / path.name)
        manifest = json.loads((signed / "manifest.json").read_text())
        amend("static/install.sh", lambda s: installer(s, base, manifest["key_id"]))
        shutil.copy2(source / "static/install.sh", public / "install.sh")
        run("sh", "-n", str(public / "install.sh"))
        (public / "install.ps1").write_text('throw "This installation distribution supports macOS on Apple silicon."\n')
        entry = (source / "skills/install.md").read_text()
        entry = entry.replace("https://www.eigenflux.ai/install.sh", base + "/install.sh").replace(
            "https://eigenflux.ai/install.ps1", base + "/install.ps1"
        )
        (public / "install.md").write_text(entry)
        subprocess.run(["go", "run", "-ldflags", "-X cli.eigenflux.ai/internal/skills.VerifyPublicKeyBase64=" + info["public_key"],
                        "./cmd/releaseverify", "--manifest", str(signed / "manifest.json"), "--bundle", str(signed)],
                       cwd=source / "cli", check=True)
    provenance = {"source_commit": PRODUCT, "distribution_commit": harness, "prefix": prefix,
                  "base_url": base, "cli_version": version, "skills_revision": manifest["revision"],
                  "skills_public_key": info["public_key"], "platforms": ["darwin/arm64"],
                  "overlays": sorted(p.name for p in audit.iterdir())}
    (public / "provenance.json").write_text(json.dumps(provenance, indent=2) + "\n")
    hashes = {p.relative_to(public).as_posix(): digest(p) for p in sorted(public.rglob("*")) if p.is_file()}
    (public / "checksums.json").write_text(json.dumps(hashes, indent=2) + "\n")
    print("Built " + base)


if __name__ == "__main__":
    main()
