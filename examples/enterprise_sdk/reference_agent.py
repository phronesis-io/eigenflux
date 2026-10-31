#!/usr/bin/env python3
"""Runnable customer-side Agent: private SDK -> durable inbox -> Agent notes.

Install the approved binary wheel first. This is customer integration code, not
the SDK implementation. It performs no external messages or model API calls.
"""

import argparse
import json
import os
from pathlib import Path
import signal
import sqlite3
import threading
import time

from eigenflux_sdk import Client, DeliveryResult


class ReferenceAgent:
    def __init__(self, inbox: Path):
        self.inbox = inbox
        inbox.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        with self.connect() as db:
            db.execute("CREATE TABLE IF NOT EXISTS inbox(batch_id TEXT PRIMARY KEY, sequence INTEGER NOT NULL, receipt TEXT NOT NULL)")
            db.execute("CREATE TABLE IF NOT EXISTS notes(batch_id TEXT NOT NULL, item_id TEXT NOT NULL, summary TEXT NOT NULL, PRIMARY KEY(batch_id,item_id))")
        os.chmod(inbox, 0o600)

    def connect(self):
        return sqlite3.connect(self.inbox, timeout=2)

    def push_to_agent(self, delivery):
        if delivery.cancelled:
            return DeliveryResult.busy()
        try:
            with self.connect() as db:
                db.execute("BEGIN IMMEDIATE")
                row = db.execute("SELECT receipt FROM inbox WHERE batch_id=?", (delivery.batch_id,)).fetchone()
                if row:
                    return DeliveryResult.accepted(row[0])
                if delivery.cancelled:
                    db.rollback()
                    return DeliveryResult.busy()
                receipt = "reference-agent:" + delivery.batch_id
                db.execute("INSERT INTO inbox VALUES(?,?,?)", (delivery.batch_id, delivery.sequence, receipt))
                # This reference Agent consumes Feed into its own notes. A real
                # host can instead durably enqueue its own Agent inbox here.
                for item in delivery.items:
                    db.execute("INSERT INTO notes VALUES(?,?,?)", (delivery.batch_id, item["item_id"], item.get("summary", "")))
                if delivery.cancelled:
                    db.rollback()
                    return DeliveryResult.busy()
            return DeliveryResult.accepted(receipt)
        except Exception:
            # A commit/transport failure is not proof of non-acceptance.
            return DeliveryResult.unknown()

    def delivery_status(self, delivery):
        try:
            with self.connect() as db:
                row = db.execute("SELECT receipt FROM inbox WHERE batch_id=?", (delivery.batch_id,)).fetchone()
            return DeliveryResult.accepted(row[0]) if row else DeliveryResult.not_accepted()
        except sqlite3.Error:
            return DeliveryResult.unknown()

    def snapshot(self):
        with self.connect() as db:
            batches = db.execute("SELECT count(*) FROM inbox").fetchone()[0]
            notes = [{"batch_id": batch, "item_id": item, "summary": summary} for batch, item, summary in db.execute("SELECT batch_id,item_id,summary FROM notes ORDER BY batch_id,item_id")]
        return {"accepted_batches": batches, "notes": notes}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--state-dir", type=Path, required=True)
    parser.add_argument("--inbox", type=Path, required=True)
    parser.add_argument("--context-file", type=Path, required=True)
    parser.add_argument("--agent-id", required=True, help="stable customer-side Agent ID")
    parser.add_argument("--runtime-name", default="reference-agent")
    parser.add_argument("--runtime-version", default="1.0.0")
    parser.add_argument("--poll-seconds", type=int, default=3600)
    parser.add_argument("--once", action="store_true", help="exit after the first durably accepted nonempty batch")
    parser.add_argument("--timeout", type=float, default=120)
    parser.add_argument("--status-file", type=Path, help="optional operational status snapshot; contains no credential or context")
    args = parser.parse_args()
    agent = ReferenceAgent(args.inbox)
    stopped = threading.Event()
    accepted = threading.Event()

    def on_status(state, reason):
        if args.status_file:
            temporary = args.status_file.with_suffix(".tmp")
            temporary.write_text(json.dumps({"state": state, "reason": reason, "at": int(time.time() * 1000)}))
            temporary.replace(args.status_file)
        if state == "delivered":
            accepted.set()

    def context_provider():
        # The B application owns its context source. Private input is not put
        # into command-line arguments or printed as part of this example.
        return json.loads(args.context_file.read_text())

    signal.signal(signal.SIGINT, lambda *_: stopped.set())
    signal.signal(signal.SIGTERM, lambda *_: stopped.set())
    with Client(base_url=os.environ["EIGENFLUX_BASE_URL"],
                app_credential=os.environ["EIGENFLUX_APP_CREDENTIAL"],
                external_agent_id=args.agent_id, runtime_name=args.runtime_name,
                runtime_version=args.runtime_version, state_dir=args.state_dir,
                feed_poll_interval_seconds=args.poll_seconds,
                context_provider=context_provider, push_to_agent=agent.push_to_agent,
                delivery_status=agent.delivery_status, on_status=on_status,
                runtime_path=os.environ.get("EIGENFLUX_SDK_RUNTIME")) as client:
        client.wait_until_registered(timeout=args.timeout)
        if args.once:
            if not accepted.wait(args.timeout):
                raise TimeoutError("no nonempty Feed accepted before timeout")
        else:
            stopped.wait()
    print(json.dumps(agent.snapshot(), ensure_ascii=False))


if __name__ == "__main__":
    main()
