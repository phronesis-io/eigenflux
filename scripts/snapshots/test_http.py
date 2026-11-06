#!/usr/bin/env python3
"""Native CLI and installer checks against a real local HTTP fixture."""
import base64
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import tarfile
import subprocess
import tempfile
import threading
import time
import urllib.parse

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("snapshot_build", HERE / "build.py")
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


def main():
    with tempfile.TemporaryDirectory(prefix="eigenflux snapshot ") as temporary:
        root = Path(temporary).resolve()
        state = {"public": None, "prefix": None, "completed": False, "requests": [], "tamper_signature": False, "capture_pending": True}
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                self.reply()

            def do_PUT(self):
                self.reply()

            def do_POST(self):
                self.reply()

            def reply(self):
                path = urllib.parse.urlsplit(self.path).path
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                state["requests"].append((self.command, path, dict(self.headers), body))
                if state["prefix"] and path.startswith("/" + state["prefix"] + "/"):
                    relative = path[len(state["prefix"]) + 2:]
                    file = state["public"] / relative
                    if ".." in Path(relative).parts or not file.is_file():
                        self.send_error(404)
                        return
                    data = file.read_bytes()
                    if state["tamper_signature"] and file.name == "manifest.json":
                        manifest = json.loads(data)
                        manifest["signature"] = "AAAA"
                        data = json.dumps(manifest).encode()
                    self.send_response(200)
                elif path == "/api/v2/agent-context":
                    if state["completed"]:
                        self.send_response(200)
                        data = b'{"code":0,"data":{"context_revision":19}}'
                    else:
                        self.send_response(409)
                        data = b'{"error":{"code":"ONBOARDING_REQUIRED","details":{"onboarding_state":"in_progress"}}}'
                elif path == "/api/v2/need-capture/pending":
                    self.send_response(200)
                    intents = [{"intent_id":"73", "intent_version":2, "watch_for":"Agent evaluation evidence", "trigger_when":"New reproducible results", "action_instruction":"Analyze", "action_policy":"analyze_only", "priority":8, "existing_inputs":[]}] if state["capture_pending"] else []
                    data = json.dumps({"code":0,"data":{"intents":intents,"has_more":False}}).encode()
                elif path == "/api/v2/need-capture/complete":
                    payload = json.loads(body)
                    assert payload["intent_id"] == "73" and payload["intent_version"] == 2
                    state["capture_pending"] = False
                    self.send_response(200)
                    data = b'{"code":0,"data":{"completed":true}}'
                elif path.startswith("/api/v2/"):
                    self.send_response(200)
                    data = b'{"code":0,"data":{}}'
                else:
                    self.send_error(404)
                    return
                self.end_headers()
                self.wfile.write(data)
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            origin = f"http://127.0.0.1:{server.server_port}"
            target_os = "darwin" if platform.system() == "Darwin" else "linux"
            arch = "arm64" if platform.machine() in ("arm64", "aarch64") else "amd64"
            build = root / "build"
            builder.make(build, origin, [target_os + "/" + arch], Path(os.environ["SNAPSHOT_HELPERS"]) if os.environ.get("SNAPSHOT_HELPERS") else None)
            public = build / "public"
            proof = json.loads((public / "provenance.json").read_text())
            state.update(public=public, prefix=proof["prefix"])
            env = {k: v for k, v in os.environ.items() if not k.startswith("EIGENFLUX_")}
            home = root / "agent home/.eigenflux"
            skills = root / "loaded skills"
            bindir = root / "bin"
            bindir.mkdir()
            binary = bindir / "eigenflux"
            # Regression: same semver, wrong bytes must still be replaced.
            binary.write_text("#!/bin/sh\nprintf '0.0.55\\n'\n")
            binary.chmod(0o755)
            # A prior independently signed release can leave its manifest after
            # managed folders are removed. Reproduce the observed sequence-33
            # failure through the real installer, without touching user files.
            foreign_stage = root / "foreign release"
            shutil.copytree(build / "skills", foreign_stage)
            for entry in foreign_stage.glob("*/SKILL.md"):
                entry.write_bytes(entry.read_bytes() + b"\nPrior channel fixture\n")
            foreign_tar = root / "foreign.tar.gz"
            with tarfile.open(foreign_tar, "w:gz") as archive:
                for directory in foreign_stage.iterdir():
                    archive.add(directory, arcname=directory.name)
            foreign_seed = root / "foreign-seed"
            foreign_seed.write_text(base64.b64encode(os.urandom(32)).decode())
            foreign_manifest = root / "foreign-manifest.json"
            subprocess.run(["go", "run", "./cmd/manifestgen", "--skills-dir", str(foreign_stage),
                            "--cli-version", proof["cli_version"], "--sequence", "33", "--signing-key-file", str(foreign_seed),
                            "--tarball", str(foreign_tar), "--out", str(foreign_manifest)],
                           cwd=build / "source/cli", check=True, capture_output=True, text=True)
            foreign_seed.unlink()
            foreign = json.loads(foreign_manifest.read_bytes())
            assert foreign["key_id"] != json.loads((public / "skills/latest/manifest.json").read_bytes())["key_id"]
            skills.mkdir()
            (skills / ".ef-manifest.json").write_bytes(foreign_manifest.read_bytes())
            unrelated = skills / "unrelated-skill/SKILL.md"
            unrelated.parent.mkdir()
            unrelated.write_text("Unrelated skill must survive")
            installer = root / "installer-functions.sh"
            installer.write_text((public / "install.sh").read_text().split("# ── Main ──", 1)[0])
            install_env = dict(env, PATH=str(bindir) + os.pathsep + env["PATH"],
                               EIGENFLUX_HOME=str(home), EIGENFLUX_SKILLS_DIR=str(skills),
                               EIGENFLUX_INSTALL_DIR=str(bindir), EIGENFLUX_CDN_URL="http://127.0.0.1:1/incorrect")
            installed = subprocess.run(["sh", "-c", '. "$1"; install_cli; verify_snapshot_cli; install_skills', "snapshot-installer", str(installer)],
                                       env=install_env, capture_output=True, text=True)
            assert installed.returncode == 0, installed.stdout + installed.stderr
            expected_binary = public / "cli" / proof["cli_version"] / ("eigenflux-" + target_os + "-" + arch)
            assert builder.digest(binary) == builder.digest(expected_binary), "same-version installer failed to replace binary"
            def call(*args, success=True, extra=None, agent_home=home):
                result = subprocess.run([str(binary), "--homedir", str(agent_home), *args],
                                        env=dict(env, **(extra or {})), cwd=root, text=True, capture_output=True)
                if success:
                    assert result.returncode == 0, result.stdout + result.stderr
                return result
            call("skills", "target", "set", "--path", str(skills), "--host", "codex")
            assert unrelated.read_text() == "Unrelated skill must survive"
            # Switching with intact old managed folders must also install the
            # requested signed snapshot, preserving unrelated skill content.
            intact_home, intact_target = root / "intact/.eigenflux", root / "intact skills"
            shutil.copytree(foreign_stage, intact_target)
            (intact_target / ".ef-manifest.json").write_bytes(foreign_manifest.read_bytes())
            call("skills", "target", "set", "--path", str(intact_target), "--host", "codex", agent_home=intact_home)
            intact_result = json.loads(call("skills", "sync", "--format", "json", agent_home=intact_home).stdout)
            assert intact_result["verified_manifest"]
            assert json.loads((intact_target / ".ef-manifest.json").read_bytes())["revision"] == proof["skills_revision"]
            for _ in range(3):
                synced = json.loads(call("skills", "sync", "--format", "json", extra={"EIGENFLUX_CDN_URL": "http://127.0.0.1:1"}).stdout)
                assert synced["verified_manifest"]
                assert json.loads((skills / ".ef-manifest.json").read_text())["revision"] == proof["skills_revision"]
            name = "nondefault-server"
            call("server", "add", "--name", name, "--endpoint", origin)
            credentials = home / "servers" / name / "agent-v2-credentials.json"
            credentials.parent.mkdir(parents=True, exist_ok=True)
            credentials.write_text(json.dumps({"access_token": "fixture-access", "refresh_token": "fixture-refresh", "agent_id": "777",
                                                "principal_id": "888", "expires_at": int((time.time() + 3600) * 1000)}))
            for completed in [False, True, True]:
                state["completed"] = completed
                result = call("--server", name, "--runtime-mode", "skill", "--runtime-model", "fixture-model", "heartbeat", "plan", "--format", "json")
                plan = json.loads(result.stdout)
                assert plan["skill_revision"] == proof["skills_revision"]
                assert plan["skills_target"] == str(skills)
                prefix = [str(binary), "--homedir", str(home), "--server", name, "--runtime-mode", "skill"]
                assert shlex.split(plan["cli_prefix"]) == prefix + ["--origin", "heartbeat"]
                assert shlex.split(plan["scheduler_launcher"]) == prefix + ["heartbeat", "plan", "--shell", "posix", "--format", "agent"]
                assert "fixture-model" not in plan["scheduler_prompt"]
                if completed:
                    order = plan["execution_order"]
                    assert order.index("commands") < order.index("need_capture") < order.index("feed")
                    assert plan["need_capture"]["start_budget_seconds"] == 120
                    assert plan["need_capture"]["max_intents"] == 2
                else:
                    assert plan["execution_order"] == ["feed"]
            # Fresh processes use normal host routing and the pinned distribution.
            # Consume the actual plan receipt, submit once, then replan for exclusion.
            receipt = plan["need_capture"]["plan_id"]
            status = json.loads(call("--server", name, "need", "capture", "status", "--plan", receipt, "--format", "json").stdout)
            assert not status["all_confirmed"]
            payload = root / "capture.json"
            payload.write_text(json.dumps({"intent_id":"73","intent_version":2,"outcome":"captured","inputs":[{"schema_version":"need_input.v2","intent_id":"73","intent_version":2,"need_type":"broadcast","target":{"goal":"Find reproducible Agent evaluation results"}}]}))
            call("--server", name, "--runtime-mode", "skill", "--runtime-model", "fixture-model", "need", "capture", "complete", "--plan", receipt, "--file", str(payload))
            status = json.loads(call("--server", name, "need", "capture", "status", "--plan", receipt, "--format", "json").stdout)
            assert status["all_confirmed"] and status["items"][0]["outcome"] == "captured"
            plan = json.loads(call("--server", name, "--runtime-mode", "skill", "--runtime-model", "fixture-model", "heartbeat", "plan", "--format", "json").stdout)
            assert plan["need_capture"]["status"] == "empty"
            # The rules_read command must use the same actual loaded directory,
            # without a wrapper-only EIGENFLUX_SKILLS_DIR environment variable.
            read_receipt = plan.get("skills_read_receipt")
            if read_receipt:
                result = subprocess.run([str(binary), "--homedir", str(home), "--server", name, "--runtime-mode", "skill", "--runtime-model", "fixture-model", "heartbeat", "maintenance-report", "--stdin", "--format", "json"], env=env, input=json.dumps(read_receipt), text=True, capture_output=True)
                assert result.returncode == 0, result.stdout + result.stderr
            for _, path, headers, _ in state["requests"]:
                if path.startswith("/api/v2/"):
                    if path in ("/api/v2/agent-context", "/api/v2/need-capture/pending", "/api/v2/need-capture/complete"):
                        assert headers.get("X-Client-Mode") == "skill", headers
                        assert headers.get("X-Client-Model") == "fixture-model", headers
            edited = skills / "ef-broadcast/SKILL.md"
            original = edited.read_bytes()
            edited.write_bytes(original + b"\nUnrelated local change\n")
            assert call("skills", "sync", success=False).returncode != 0, "modified Skills accepted"
            edited.write_bytes(original)
            manifest_file = skills / ".ef-manifest.json"
            original_manifest = manifest_file.read_bytes()
            stale = json.loads(original_manifest)
            stale.update(sequence=999999, revision="other-source")
            manifest_file.write_text(json.dumps(stale))
            assert call("skills", "sync", success=False).returncode != 0, "wrong higher-sequence fallback accepted"
            manifest_file.write_bytes(original_manifest)
            reused = json.loads(original_manifest)
            reused["revision"] = "different-content-at-same-sequence"
            manifest_file.write_text(json.dumps(reused))
            rejected = call("skills", "sync", success=False)
            assert rejected.returncode != 0 and "sequence reused" in rejected.stderr, rejected.stderr
            manifest_file.write_bytes(original_manifest)
            state["tamper_signature"] = True
            fresh_home, fresh_target = root / "fresh/.eigenflux", root / "fresh skills"
            call("skills", "target", "set", "--path", str(fresh_target), agent_home=fresh_home)
            assert call("skills", "sync", success=False, agent_home=fresh_home).returncode != 0, "invalid signature accepted"
            # No grading/test instructions were added to the public entry.
            entry = (public / "install.md").read_text().replace(proof["base_url"] + "/install.sh", "https://www.eigenflux.ai/install.sh").replace(proof["base_url"] + "/install.ps1", "https://eigenflux.ai/install.ps1")
            assert entry == (build / "source/skills/install.md").read_text()
            print("PASS: capture submission/exclusion, local receipts, same-directory rules_read, real HTTP install with signed foreign sequence-33 residue, intact channel switch, unrelated-skill preservation, repeated signed sync, lifecycle plans, same-authority rollback/reuse, drift/signature rejection and unchanged entry prose")
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    main()

