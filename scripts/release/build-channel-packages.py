#!/usr/bin/env python3
"""Build uploadable channel packages from the maintained setup and installer."""
import argparse
from pathlib import Path
import shutil
import subprocess
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[2]
CHANNELS = {
    "skillhub": ("eigenflux-skillhub", ""),
    "xhs": ("eigenflux-skillhub-xhs", "EF-1PoI4QXu"),
}


def build(output: Path, selected: str = "all") -> list[Path]:
    output = output.resolve()
    names = list(CHANNELS) if selected == "all" else [selected]
    targets = [output / CHANNELS[name][0] for name in names]
    for target in targets:
        if target.exists() or target.with_suffix(".zip").exists():
            raise FileExistsError(f"Refusing to replace existing channel output: {target}")
    output.mkdir(parents=True, exist_ok=True)
    source = subprocess.check_output(
        ["git", "rev-parse", "HEAD"], cwd=ROOT, text=True
    ).strip()
    template = (ROOT / "channels/skillhub/scripts/install-eigenflux.sh.in").read_text()
    built = []
    for name, target in zip(names, targets):
        _, referral = CHANNELS[name]
        with tempfile.TemporaryDirectory(prefix=".channel-", dir=output) as staging:
            package = Path(staging) / target.name
            scripts = package / "scripts"
            scripts.mkdir(parents=True)
            shutil.copyfile(ROOT / "channels/skillhub/SKILL.md", package / "SKILL.md")
            shutil.copyfile(ROOT / "static/install.sh", scripts / "install.sh")
            wrapper = scripts / "install-eigenflux.sh"
            wrapper.write_text(template.replace("@@DEFAULT_REF@@", referral))
            for script in scripts.iterdir():
                script.chmod(0o755)
            (package / "README.md").write_text(
                f"# EigenFlux channel package\n\n"
                f"Channel: {name}\n\nSource commit: {source}\n\n"
                f"Default referral: {referral or 'none'}\n\n"
                "Upload this entire directory or its zip archive. The setup Skill "
                "uses the bundled official installer and the current dynamically "
                "synchronized Skills. Business Skills are not bundled.\n\n"
                "A new Agent receives its V2 identity during onboarding, then the "
                "human verifies and binds email in Console V2.\n"
            )
            archive = Path(staging) / target.with_suffix(".zip").name
            with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as z:
                for path in sorted(package.rglob("*")):
                    if path.is_file():
                        z.write(path, path.relative_to(package.parent))
            package.rename(target)
            archive.rename(target.with_suffix(".zip"))
        built.append(target)
    return built


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True, type=Path)
    parser.add_argument("--channel", choices=["all", *CHANNELS], default="all")
    args = parser.parse_args()
    for target in build(args.output_dir, args.channel):
        print(target)
        print(target.with_suffix(".zip"))


if __name__ == "__main__":
    main()
