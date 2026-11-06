# Need phase-one immutable installation snapshot

Product source: `3b08f41d25f41108b89f8f5add1ba369b5387525` (CLI 1.0.18).
Distribution changes live only on `codex/need-phase1-snapshot-r2`.
The public entry is `snapshots/<product SHA>/<distribution SHA>/install.md`.

This adapts the historical immutable publisher at
`6483f72f6aad981eaa3037bd7a1276801cfb10bc` to the current installer and signed
complete-package updater. It creates a fresh ephemeral signing authority,
publishes macOS/Linux binaries plus signed Skills and complete ZIP packages,
and pins future automatic synchronization to the same immutable CDN prefix.
It does not install components, bind identities or create tasks on the builder.

Public macOS builds require the existing verified, notarized notification App
at the product's independent NOTIFIER_VERSION. The workflow restores it read-only
from its signed immutable receipt and revalidates Apple identity and tickets.
It never publishes or changes production notification artifacts. Only loopback
HTTP fixtures may build an ad-hoc helper, and those artifacts are never published.

Run `python3 scripts/snapshots/test_contract.py` and
`python3 scripts/snapshots/test_http.py`, followed by the CLI suite. The HTTP
fixture runs the actual installer functions and fresh native subprocesses in
temporary Homes, with signed channel switching, version/hash checks, loaded
Skills, Need submission/receipts, later Pending exclusion and drift rejection.
Native Codex permissions, restart, website setup and real model extraction still
require user acceptance. Tests do not create real server accounts.

`provenance.json`, `checksums.json` and the workflow audit artifact identify the
exact source, binary and Skill modifications. The installer entry is the product
document with distribution URLs replaced only. Existing accounts may enter the
product's resume flow; installation isolation does not isolate server data.
