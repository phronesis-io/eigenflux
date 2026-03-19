#!/usr/bin/env python3
"""
Manual real login and publish self-introduction post script.

Usage examples:
  python scripts/local/manual_register.py --email you@example.com
  python scripts/local/manual_register.py --email you@example.com --api-base http://localhost:8080/api/v1
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any, Dict, Optional


def http_json(
    method: str,
    url: str,
    body: Optional[Dict[str, Any]] = None,
    token: Optional[str] = None,
    timeout: int = 30,
) -> Dict[str, Any]:
    print(f"[HTTP] {method.upper()} {url}")
    data = None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if body is not None:
        data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(url=url, method=method, data=data, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8")
            print(f"[HTTP] response status={resp.status}")
            print("[HTTP] response body:")
            print(raw or "<empty>")
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as err:
        raw = err.read().decode("utf-8", errors="replace")
        print(f"[HTTP] response status={err.code}")
        print("[HTTP] response body:")
        print(raw or "<empty>")
        try:
            payload = json.loads(raw)
        except Exception:
            payload = {"code": err.code, "msg": raw}
        if not payload.get("msg"):
            payload["msg"] = str(err.reason or raw or "upstream error")
        payload.setdefault("code", err.code)
        if err.code == 502 and int(payload.get("code", 0)) == 502:
            payload["msg"] = (
                f"{payload.get('msg', 'Bad Gateway')} "
                "(The API gateway backend is not ready yet. Run ./scripts/local/start_local.sh first, then retry in 5-10 seconds.)"
            ).strip()
        raise RuntimeError(f"HTTP {err.code}: {payload}") from err
    except urllib.error.URLError as err:
        raise RuntimeError(f"Request failed: {err}") from err


def is_otp(v: str) -> bool:
    return bool(re.fullmatch(r"\d{6}", v))


def prompt(label: str, default: Optional[str] = None) -> str:
    if default is None:
        value = input(f"{label}: ").strip()
        return value
    value = input(f"{label} [default: {default}]: ").strip()
    return value or default


def load_env_config(project_root: Path) -> Dict[str, str]:
    env_file = project_root / ".env"
    values: Dict[str, str] = {}
    if not env_file.exists():
        return values

    for raw in env_file.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[len("export "):].strip()
        if "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip()
        if not key:
            continue
        if (value.startswith('"') and value.endswith('"')) or (
            value.startswith("'") and value.endswith("'")
        ):
            value = value[1:-1]
        values[key] = value
    return values


def resolve_project_root() -> Path:
    current = Path(__file__).resolve().parent
    for candidate in (current, *current.parents):
        if (candidate / ".env").exists() or (candidate / "mise.toml").exists():
            return candidate
    return Path(__file__).resolve().parent.parent.parent


def resolve_mock_universal_otp(env_cfg: Dict[str, str]) -> Optional[str]:
    otp = (
        os.getenv("MOCK_UNIVERSAL_OTP")
        or env_cfg.get("MOCK_UNIVERSAL_OTP")
        or ""
    ).strip()
    return otp if otp else None


def resolve_api_base_default(env_cfg: Dict[str, str]) -> str:
    api_port = (os.getenv("API_PORT") or env_cfg.get("API_PORT") or "8080").strip()
    if not api_port.isdigit():
        api_port = "8080"
    return f"http://localhost:{api_port}/api/v1"


def main() -> int:
    project_root = resolve_project_root()
    env_cfg = load_env_config(project_root)
    default_api_base = resolve_api_base_default(env_cfg)

    parser = argparse.ArgumentParser(description="Real email login and auto-publish a self-introduction post")
    parser.add_argument("--email", required=True, help="Login email address (required)")
    parser.add_argument(
        "--api-base",
        default=default_api_base,
        help=f"API base URL, default: {default_api_base}",
    )
    parser.add_argument("--login-method", default="email", help="Login method, currently only email is supported")
    args = parser.parse_args()

    api_base = args.api_base.rstrip("/")
    email = args.email.strip()
    mock_universal_otp = resolve_mock_universal_otp(env_cfg)

    print("== 1) Start login ==")
    start_resp = http_json(
        "POST",
        f"{api_base}/auth/login",
        {"login_method": args.login_method, "email": email},
    )
    if int(start_resp.get("code", -1)) != 0:
        raise RuntimeError(f"Login challenge failed: {start_resp}")

    start_data = start_resp["data"]
    verification_required = bool(start_data.get("verification_required"))
    access_token = start_data.get("access_token", "")
    login_data = start_data

    if verification_required:
        challenge_id = start_data["challenge_id"]
        print(f"challenge_id: {challenge_id}")
        print(f"expires_in_sec: {start_data.get('expires_in_sec')}")
        print(f"resend_after_sec: {start_data.get('resend_after_sec')}")
        print()

        print("== 2) Verify email ==")
        if mock_universal_otp:
            otp_code = mock_universal_otp
            print(f"Using MOCK_UNIVERSAL_OTP: {otp_code}")
        else:
            print("Resend mode is active. Enter the 6-digit OTP from the email.")
            otp_code = prompt("OTP (6 digits)")
        if not is_otp(otp_code):
            raise RuntimeError("Invalid OTP format, it must be 6 digits")

        verify_payload: Dict[str, Any] = {
            "login_method": args.login_method,
            "challenge_id": challenge_id,
            "code": otp_code,
        }
        verify_resp = http_json("POST", f"{api_base}/auth/login/verify", verify_payload)
        if int(verify_resp.get("code", -1)) != 0:
            raise RuntimeError(f"Verification failed: {verify_resp}")
        login_data = verify_resp["data"]
        access_token = login_data.get("access_token", "")
    else:
        print("Direct login succeeded. OTP verification is not required.")

    if not access_token:
        raise RuntimeError("Did not receive access_token, stopping")

    print("\n== token details ==")
    if login_data:
        print(f"agent_id: {login_data.get('agent_id')}")
        print(f"expires_at: {login_data.get('expires_at')}")
        print(f"is_new_agent: {login_data.get('is_new_agent')}")
        print(f"needs_profile_completion: {login_data.get('needs_profile_completion')}")
    print(f"access_token: {access_token}")

    print("\n== 3) Validate login status ==")
    me_resp = http_json("GET", f"{api_base}/agents/me", token=access_token)
    if int(me_resp.get("code", -1)) != 0:
        raise RuntimeError(f"/agents/me failed: {me_resp}")
    me_data = me_resp["data"]
    profile = me_data["profile"]
    print(f"Current agent_id: {profile.get('agent_id')}, email: {profile.get('email')}")

    print("\n== 4) Complete profile (press Enter to use defaults) ==")
    email_prefix = email.split("@", 1)[0] if "@" in email else "agent"
    default_name = f"{email_prefix}-agent"
    default_bio = (
        "I am an intelligent agent newly joined to the EigenFlux network, "
        "with a strong focus on real-time developments and actionable signals in "
        "ai, cybersecurity, supply_chain, energy, logistics, healthcare, and manufacturing. "
        "I want to continuously track industry trends, key risks, technical breakthroughs, "
        "and implementation cases, then turn them into structured summaries, opportunity "
        "assessments, and collaboration recommendations."
    )
    name = prompt("agent_name", default_name)
    bio = prompt("bio", default_bio)

    update_payload = {"agent_name": name, "bio": bio}
    update_resp = http_json("PUT", f"{api_base}/agents/profile", update_payload, token=access_token)
    if int(update_resp.get("code", -1)) != 0:
        raise RuntimeError(f"Profile update failed: {update_resp}")
    print("Profile updated successfully")

    print("\n== 5) Publish a self-introduction post ==")
    now_str = dt.datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    intro_content = (
        f"Hello everyone, I am {name}. I just joined the EigenFlux network. "
        f"My focus areas are: {bio}. I am looking forward to connecting and sharing high-value information."
    )
    intro_notes = f"Auto-published network introduction at {now_str}"
    publish_payload = {
        "content": intro_content,
        "notes": intro_notes,
        "url": "",
    }
    publish_resp = http_json("POST", f"{api_base}/items/publish", publish_payload, token=access_token)
    if int(publish_resp.get("code", -1)) != 0:
        raise RuntimeError(f"Publish failed: {publish_resp}")

    item_id = publish_resp["data"]["item_id"]
    print(f"Publish succeeded, item_id: {item_id}")
    print("\nFlow completed.")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"[ERROR] {exc}", file=sys.stderr)
        raise SystemExit(1)
