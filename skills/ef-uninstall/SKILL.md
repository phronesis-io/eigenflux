---
name: ef-uninstall
description: Remove an EigenFlux CLI installation, its owned native triggers, host plugin, and managed Skills when the owner requests uninstallation. Delete EigenFlux-owned local identity, credentials, data, and edited content.
metadata:
  author: "Phronesis AI"
  version: "1.0.8"
  requires:
    bins: ["eigenflux"]
---

# EigenFlux Uninstall

Handle owner requests to uninstall or remove EigenFlux, including “卸载 EigenFlux”. Require CLI 1.0.31. Use the stable Agent Home and resolved absolute CLI path.

Preview through the official `<CLI>.uninstall.sh` or `<CLI>.uninstall.ps1` beside the CLI. Pass the absolute CLI path first, then `--homedir`, `--server`, `--source agent`, and `--format json`. Inspect registered Homes, hosts, Skills targets, and preserved content. For a missing wrapper, recover ownership through CLI preview and run `installation record` with the verified host and Skills target. Use the registered wrapper paths; retain unknown paths.

Before cleanup, ask whether other Agents still need EigenFlux. Wait for the owner to choose current-Agent removal or complete removal; use an already explicit scope without asking again. Select `--scope current` when others still need EigenFlux and `--scope all` for complete removal. Carry the selected scope through preview, `--begin`, `--attempt`, result recording, and apply.

Ask for the uninstall reason when confirming scope. Tell the owner the text is uploaded to the selected EigenFlux server before cleanup. Reuse a reason already supplied. Require owner-provided text or an explicit decline. Treat silence as pending.

Run the wrapper with `--begin`. Pass the owner's exact text on standard input with `--reason-stdin` and set `--reason-category` to `usability`, `cost`, `performance`, `privacy`, `no_longer_needed`, or `other` based on the response. Pass `--reason-category declined` for an explicit decline. Wait for `reason_uploaded: true` before native cleanup. On upload failure, resolve connectivity or authentication and retry `--begin --attempt` with the same response. Carry the confirmed ID through `--attempt`. Record native cleanup failures through `--record-result blocked` or `--record-result failed`.

For current-Agent removal, stop only the current Home's watch owners. Use host-native tools to remove only its EigenFlux triggers, plugin bindings, and configuration. Preserve plugin packages, Skills, notification integrations, and other resources still used by another Agent. For complete removal, stop and remove all registered owners with their native tools, matching task identity, Home, and server. Preserve unrelated tasks and shared configuration. Resolve ambiguous ownership with the owner.

For current-Agent removal, preserve Skills targets outside the current Home. Remove only private targets within the current Home that have no registered shared use.

For complete Codex removal, use the verified Codex executable and its `CODEX_HOME` for both plugin and marketplace cleanup. Resolve an unavailable executable through [the installation guide](https://cdn.eigenflux.ai/skills/latest/install.md#codex); retain the same Codex Home. Inspect the `eigenflux` marketplace configuration and `$CODEX_HOME/.tmp/marketplaces/eigenflux`, even when `plugin marketplace list` omits it. Verify available configuration and checkout Git origin against the official `phronesis-io/codex-eigenflux` source; cross-check `.codex-marketplace-install.json` when present. Require the catalog to contain only `codex-eigenflux`. Preserve conflicting, unverified, or shared sources and report cleanup as blocked.

Remove the installed `codex-eigenflux@eigenflux` plugin, then run `codex plugin marketplace remove eigenflux` with that same executable and Home. Use marketplace removal for a verified orphaned root even when the plugin and configuration entry are already absent. Never substitute manual `config.toml` deletion for marketplace removal. Verify the plugin, marketplace registration, and installed marketplace root are absent before confirming host cleanup. For current-Agent removal, retain shared marketplace registration and its root.

Remove EigenFlux-owned local accounts, credentials, identity keys, caches, history, edited Skills, scripts, native triggers, and plugin configuration. Use the selected scope. Preserve unrelated host configuration.

For complete Windows removal, inspect `%USERPROFILE%\.agents\skills` for legacy installations without a managed manifest. Verify EigenFlux ownership from each Skill's `SKILL.md` and remove those directories with host-native tools. Preserve unverified directories.

After native cleanup succeeds, run the wrapper with `--apply --host-cleanup-confirmed`. Include `--notifications --notification-cleanup-confirmed` only for complete removal when scope covers all notification users. Preserve shared or changed notification assets. Verify Windows finalization after the original CLI exits.

Read back native state. For current-Agent removal, verify current Home data and registration were removed and the shared CLI and other Agents remain usable. For complete removal, verify executable removal. Verify local credentials, configuration, cached history, registered Skills, and scripts were deleted. Report permission failures and partial or blocked cleanup. Retain shared lock files and unrelated host content.
