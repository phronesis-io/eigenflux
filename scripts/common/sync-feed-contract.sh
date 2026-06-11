#!/usr/bin/env bash
# Sync the feed output contract digest from the canonical skill source into the
# API static assets. The backend reads static/feed_contract.md at runtime and
# delivers it in the feed response (output_contract), so every client — the
# bare CLI, the OpenClaw plugin, and the Claude Code plugin — inherits it from
# one source. Keep skills/ef-broadcast/references/contract.md as the only
# hand-edited copy; this file is generated.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
src="$repo_root/skills/ef-broadcast/references/contract.md"
dst="$repo_root/static/feed_contract.md"

if [[ ! -f "$src" ]]; then
  echo "sync-feed-contract: source not found: $src" >&2
  exit 1
fi

cp "$src" "$dst"
echo "sync-feed-contract: $src -> $dst"
