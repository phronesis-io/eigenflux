#!/usr/bin/env python3
"""Probe root-plugin discovery with a real Codex CLI in a disposable Home.

Usage: python3 scripts/tests/check-codex-marketplace.py /path/to/codex supported
Use `unsupported` for the 0.139.0 regression fixture. No plugin is installed.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile


def check(binary, expected):
    binary = str(Path(binary).resolve(strict=True))
    with tempfile.TemporaryDirectory(prefix="eigenflux codex probe ") as temporary:
        root = Path(temporary)
        market = root / "marketplace"
        catalog = market / ".agents/plugins/marketplace.json"
        manifest = market / ".codex-plugin/plugin.json"
        catalog.parent.mkdir(parents=True)
        manifest.parent.mkdir(parents=True)
        (market / ".git").mkdir()
        catalog.write_text(json.dumps({"name": "eigenflux-probe", "plugins": [{
            "name": "codex-eigenflux", "source": {"source": "local", "path": "."},
            "policy": {"installation": "AVAILABLE", "authentication": "ON_USE"},
            "category": "Productivity",
        }]}))
        manifest.write_text(json.dumps({"name": "codex-eigenflux", "version": "0.1.0"}))
        home = root / "home"
        home.mkdir()
        host = root / "nondefault codex home"
        host.mkdir()
        env = {"HOME": str(home), "CODEX_HOME": str(host),
               "PATH": os.environ.get("PATH", "/usr/bin:/bin")}

        def run(*args):
            return subprocess.run([binary, *args], env=env, cwd=root,
                                  capture_output=True, text=True, check=True, timeout=60)

        version = run("--version").stdout.strip()
        run("plugin", "marketplace", "add", str(market))
        result = run("plugin", "list", "--marketplace", "eigenflux-probe", "--available", "--json")
        payload = json.loads(result.stdout)
        found = "codex-eigenflux" in json.dumps(payload)
        if found != (expected == "supported"):
            raise AssertionError(f"{version}: expected {expected}, got {payload}; {result.stderr}")
        print(f"{version}: root-directory plugin {expected}; isolated Home; no plugin installed")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("expected", choices=("supported", "unsupported"))
    args = parser.parse_args()
    check(args.binary, args.expected)
