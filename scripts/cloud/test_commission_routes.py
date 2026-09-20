#!/usr/bin/env python3
"""Exercise both public Caddy configurations against isolated HTTP upstreams."""

import http.client
import http.server
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import unittest


REPO = Path(__file__).resolve().parents[2]


class Upstream(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.rfile.read(int(self.headers.get("Content-Length", "0")))
        body = json.dumps([self.server.label, self.command, self.path]).encode()
        missing = self.server.label == "gateway" and self.path.startswith(
            "/api/v1/order-preparations"
        )
        self.send_response(404 if missing else 200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_POST = do_GET

    def log_message(self, *_args):
        pass


def replace_dials(node, replacements):
    if isinstance(node, dict):
        if "dial" in node:
            node["dial"] = replacements[node["dial"]]
        for value in node.values():
            replace_dials(value, replacements)
    elif isinstance(node, list):
        for value in node:
            replace_dials(value, replacements)


class CommissionRoutesTest(unittest.TestCase):
    def setUp(self):
        self.binary = os.environ.get("CADDY_BIN") or shutil.which("caddy")
        if not self.binary:
            raise RuntimeError("Install Caddy or set CADDY_BIN before running this test")
        self.temp = tempfile.TemporaryDirectory(prefix="eigenflux-commission-routes-")
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.dials = {}
        for label, ports in (("commission", (8090,)), ("gateway", (8080, 8088, 3000))):
            server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
            server.label = label
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            self.addCleanup(server.server_close)
            self.addCleanup(server.shutdown)
            for port in ports:
                self.dials[f"127.0.0.1:{port}"] = f"127.0.0.1:{server.server_port}"

    def start_caddy(self, filename):
        adapted = subprocess.run(
            [self.binary, "adapt", "--config", str(REPO / filename), "--adapter", "caddyfile"],
            check=True, capture_output=True, text=True,
        )
        servers = json.loads(adapted.stdout)["apps"]["http"]["servers"]
        self.assertEqual(len(servers), 1)
        server = next(iter(servers.values()))
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            self.port = listener.getsockname()[1]
        # Preserve the adapted routing tree; isolate listeners, TLS, logs and upstreams.
        server["listen"] = [f"127.0.0.1:{self.port}"]
        server["automatic_https"] = {"disable": True}
        server.pop("tls_connection_policies", None)
        server.pop("logs", None)
        replace_dials(server, self.dials)
        config = self.directory / "caddy.json"
        config.write_text(json.dumps({
            "admin": {"disabled": True},
            "apps": {"http": {"servers": {"test": server}}},
        }), encoding="utf-8")
        log = (self.directory / "caddy.log").open("w+b")
        self.addCleanup(log.close)
        process = subprocess.Popen(
            [self.binary, "run", "--config", str(config)],
            cwd=self.directory, stdin=subprocess.DEVNULL, stdout=log, stderr=log,
            env={**os.environ, "XDG_CONFIG_HOME": str(self.directory / "config"),
                 "XDG_DATA_HOME": str(self.directory / "data")},
        )

        def stop():
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)

        self.addCleanup(stop)
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if process.poll() is not None:
                log.seek(0)
                self.fail(log.read().decode())
            try:
                if self.request("GET", "/api/_ready")[0] == 200:
                    return
            except (OSError, http.client.HTTPException):
                pass
            time.sleep(0.05)
        self.fail("Local Caddy did not become ready")

    def request(self, method, path):
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=2)
        try:
            connection.request(method, path, headers={"Host": "www.eigenflux.ai"})
            response = connection.getresponse()
            return response.status, json.loads(response.read())
        finally:
            connection.close()

    def check_routes(self, filename):
        self.start_caddy(filename)
        cases = [
            ("GET", "/api/v1/public/commissions/42", "commission"),
            ("GET", "/api/v1/public/commissions/42/reviews?commission_version=2&cursor=next", "commission"),
            ("GET", "/api/v1/public/unrelated", "gateway"),
            ("GET", "/api/v1/public/commissions-other/42", "gateway"),
            ("POST", "/api/v1/order-preparations", "commission"),
            ("GET", "/api/v1/order-preparations/51", "commission"),
            ("POST", "/api/v1/order-preparations/51/uploads", "commission"),
            ("POST", "/api/v1/order-preparations/51/uploads/confirm", "commission"),
            ("GET", "/api/v1/order-preparations/51/download?logical_path=inputs%2Frequest.txt", "commission"),
            ("POST", "/api/v1/orders", "commission"),
            ("POST", "/api/v1/orders/51/uploads", "commission"),
            ("GET", "/api/v1/commissions/42", "commission"),
            ("GET", "/api/v1/commissions/search?q=test", "gateway"),
            ("GET", "/api/v1/commissions/recommendations", "gateway"),
            ("GET", "/api/v1/profile", "gateway"),
        ]
        for method, path, upstream in cases:
            with self.subTest(method=method, path=path):
                self.assertEqual(self.request(method, path), (200, [upstream, method, path]))

    def test_development_routes(self):
        self.check_routes("Caddyfile.dev")

    def test_production_routes(self):
        self.check_routes("Caddyfile.prod")


if __name__ == "__main__":
    unittest.main(verbosity=2)
