---
name: ef-uninstall
description: Remove an EigenFlux CLI installation, its owned native triggers, host plugin, and unchanged managed Skills when the owner requests uninstallation. Preserve Agent identity and data.
metadata:
  author: "Phronesis AI"
  version: "1.0.0"
  requires:
    bins: ["eigenflux"]
---

# EigenFlux Uninstall

Require CLI 1.0.0. Use the owner's stable Agent Home and the resolved absolute CLI path.

Run `eigenflux uninstall --format json` to inspect registered Homes, hosts, executable, and Skills targets. Resolve a missing installation record from verified installation evidence through `installation record --host`; preserve unknown paths.

Use each registered host's native scheduler and plugin tools to stop and remove only the owned EigenFlux heartbeat and plugin. Match task identity, purpose, Home, and server. Preserve unrelated tasks and shared host configuration. Stop watch owners before removing files. Resolve ambiguous ownership with the owner.

Preserve cloud accounts, credentials, history, and user-edited Skills. Treat shared binaries and Skills targets as shared across every listed Home. Use `--all-homes` only when the requested removal covers all registered Homes. Include `--skills` only when Skills removal is authorized.

Run the inspected uninstall with `--apply --host-cleanup-confirmed` after native cleanup succeeds. On Windows invoke the distributed `uninstall.ps1` with the exact executable path so removal occurs after the CLI exits. On POSIX use the CLI or distributed `uninstall.sh` with that path. Keep permission failures visible and preserve incomplete operations for retry.

Pass a voluntarily supplied reason through `--reason`. Keep it optional and local to the retained Home. Finish removal without collecting a reason when none was supplied.

Read back native task/plugin state and verify executable removal. Report preserved edited Skills, shared PATH entries, retained data, and any blocked operation. Distinguish partial cleanup from completed removal.
