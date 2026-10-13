---
name: ef-uninstall
description: Remove an EigenFlux CLI installation, its owned native triggers, host plugin, and unchanged managed Skills when the owner requests uninstallation. Preserve Agent identity and data.
metadata:
  author: "Phronesis AI"
  version: "1.0.2"
  requires:
    bins: ["eigenflux"]
---

# EigenFlux Uninstall

Handle owner requests to uninstall or remove EigenFlux, including “卸载 EigenFlux”. Require CLI 0.6.3. Use the stable Agent Home and resolved absolute CLI path.

Preview through the official `<CLI>.uninstall.sh` or `<CLI>.uninstall.ps1` beside the CLI. Pass the absolute CLI path first, then `--homedir`, `--server`, `--source agent`, and `--format json`. Inspect registered Homes, hosts, Skills targets, and preserved content. For a missing wrapper, recover ownership through CLI preview and run `installation record` with the verified host and Skills target. Preserve edited wrappers and unknown paths.

Run the wrapper with `--begin` before native cleanup. Carry its ID through `--attempt`. Record native cleanup failures through `--record-result blocked` or `--record-result failed`.

Stop watch owners. Use host-native tools to remove owned EigenFlux triggers and plugins, matching task identity, Home, and server. Preserve unrelated tasks and shared configuration. Resolve ambiguous ownership with the owner.

Preserve accounts, credentials, history, and edited Skills. Use `--all-homes` when authorized scope covers every registration; include `--skills` when Skills removal is authorized.

After native cleanup succeeds, run the wrapper with `--apply --host-cleanup-confirmed`. Include `--notifications --notification-cleanup-confirmed` when scope covers all notification users. Preserve shared or changed notification assets. Verify Windows finalization after the original CLI exits.

Pass voluntary reason text through `--reason` and a known category through `--reason-category`. Keep text local. Proceed without asking when no reason was supplied. Report upload failures separately; offline delivery after removal is not guaranteed.

Read back native state and verify executable removal. Report preserved content, retained data, permission failures, and partial or blocked cleanup accurately.
