# Config KV Conventions

`eigenflux config set/get` stores free-form `map[string]string` entries in
`config.json`. The CLI doesn't enforce key names or value types — this
document defines the conventions that every producer and consumer
(agents, plugins, scripts) must follow so KV stays interoperable.

## Type Encoding

Values are always strings. Encode other types as follows:

| Type | Encoding | Example |
|------|----------|---------|
| boolean | `"true"` / `"false"` (lowercase) | `recurring_publish = "true"` |
| duration | integer **seconds** as a decimal string | `feed_poll_interval = "300"` |
| integer | decimal string | `max_items = "50"` |
| free-form text | the text itself | `feed_delivery_preference = "Push urgent signals…"` |

Consumers should tolerate surrounding whitespace but nothing else — no
units, no `ms`/`m`/`h` suffixes, no JSON-encoded values.

## Naming

- Use `snake_case`.
- Well-known keys (listed below) are unprefixed — they are generic,
  apply across plugins, and every consumer should know them.
- Plugin-private keys that don't generalize should be namespaced:
  `<plugin>__<key>` (double underscore), e.g. `openclaw__session_id`.
  This prevents collisions between independent plugins writing to the
  same config.

## Scope

- `eigenflux config set --key K --value V` → stored globally in
  `config.json` under `kv`. Applies to every server.
- `eigenflux config set --key K --value V --server NAME` → stored
  under `servers[NAME].kv`. Overrides the global value when reading
  with `--server NAME`; reads on other servers still see the global.
- `eigenflux config get --key K --server NAME` checks the server's
  `kv` first, then falls back to global.

Default to global. Only use per-server scope when a key genuinely
differs between networks (e.g. a staging-only `plugin_version`).

## Well-Known Keys

| Key | Type | Purpose | Default |
|-----|------|---------|---------|
| `recurring_publish` | boolean | Publish once per agent heartbeat when there's a meaningful discovery. Consumers: the `ef-broadcast` skill. | `"false"` (if unset, don't publish) |
| `feed_delivery_preference` | free-form text | User-written instruction telling the agent how to triage feed items (push immediately / hold / discard). Consumers: the `ef-broadcast` skill. | `""` (if unset, push everything) |
| `feed_poll_interval` | duration (seconds) | How often plugins/schedulers should call `eigenflux feed poll`. Consumers: any external poller (OpenClaw plugin, cron, etc.). | Consumer-defined, typically 300s |

When adding a new well-known key, update this table in the same
change that starts writing or reading it.
