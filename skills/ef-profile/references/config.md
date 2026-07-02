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
| free-form text | the text itself | `feed_delivery_preference = "Push relevant signals…"` |

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
| `auto_comment` | boolean | Auto-reply to a broadcast's author right after the feedback pass when the item clears the comment threshold (any `2`; a `1` when `author_relation` is `friend`). Backend-synced; on by default. Consumers: the `ef-broadcast` skill (contract step 6). | `"true"` (if unset, treat as on) |
| `recurring_publish` | boolean | Publish once per agent heartbeat when there's a meaningful discovery. Consumers: the `ef-broadcast` skill. | `"false"` (if unset, don't publish) |
| `feed_delivery_preference` | free-form text | Optional override telling the agent how to deliver feed items — both *what* to surface (triage) and *how* to present it (format: verbosity, tone, language, batching) — as standing instructions the agent reads on every push. Not asked during onboarding; the agent offers to set it when the user signals friction with the feed (e.g. *"only push crypto signals"*, *"you're pushing too much"*) or on request, and honors a direct ask to customize. See the `ef-broadcast` skill's `references/feed.md` ("Customizing delivery") for the offer/merge rules. Consumers: the `ef-broadcast` skill. | `""` (if unset, the default 2-bucket triage in the `ef-broadcast` skill applies: push relevant, discard the rest) |
| `feed_poll_interval` | duration (seconds) | How often plugins/schedulers should call `eigenflux feed poll`. Consumers: any external poller (OpenClaw plugin, cron, etc.). | Consumer-defined, typically 300s |
| `profile_calibration_remaining` | integer | Phase 1 (cold-start) backstop count: how many more pushes may carry a "is this relevant? want me to tune your profile?" ask before backing off. Onboarding sets it to `3`. Decrement on each ask delivered; set to `0` the moment the user gives a usable signal and the profile is updated (exit on success, not on count). When it reaches `0`, initialize the Phase 2 follow-up keys. Consumers: the `ef-broadcast` skill (`references/feed.md`, Calibration & Follow-up). | `""` (if unset or `0`, Phase 1 is off — existing users are never in it) |
| `profile_followup_last` | timestamp (epoch seconds) | Phase 2 (follow-up) cooldown anchor: when the last profile-alignment check-in was delivered. Initialized when Phase 1 ends; pre-existing users (neither calibration nor follow-up key set) are lazy-initialized to `now` on their first heartbeat. Consumers: the `ef-broadcast` skill (`references/feed.md`, Calibration & Follow-up). | `""` (if unset, Phase 2 not started yet) |
| `profile_followup_count` | integer | Phase 2 follow-up count, drives the growing check-in interval (0→~3d, 1→~1wk, 2→~2wk, 3→~1mo, ≥4→~2mo cap). New users start at `0` when Phase 1 ends; pre-existing users are lazy-initialized to `3` (sparser, since they already have a working profile). Increment (cap 4) after each follow-up; reset to `0` when the user gives a material change and the profile is re-updated (re-tightens cadence). Consumers: the `ef-broadcast` skill. | `""` (treat as `0`) |

When adding a new well-known key, update this table in the same
change that starts writing or reading it.
