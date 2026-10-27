# Client feedback completion

CLI 0.7.0 keeps `data/feed-reports.json` under the selected server's Agent Home.
The scope includes the server endpoint and authenticated Agent. Reports are
keyed by impression and item, retained for eight days, and written by atomic
rename under an OS advisory lock. Lock files remain after release. Corrupt
report or queue files are preserved and reported instead of being overwritten.

`feed status` lists up to 20 pending broadcast exposures from cached V2 Feed
responses in the last 48 hours, their original impressions, ages and 80-character
titles. Its totals include the entire pending set. Baseline and discovery/search
pages are excluded. Queued, accepted, duplicate and rejected feedback all count
as judged. Surface reports suppress repeat presentation across impressions.

## Scheduled pull holds

A V2 `feed poll` can hold only with `--origin heartbeat`, refresh action, the
server-scoped `feed_backlog_mode=hold` setting, completed context and a prior
non-baseline pull. It returns pending items from their original cached pages,
including per-item `impression_id` and `already_surfaced`, and copies the cached
output contract. Current owner context is synchronized before any hold. Platform
notifications are fetched through `/api/v2/notifications/pending`.

The `backlog` object exposes `held`, `pending_items`, `oldest_pending_age_ms`,
and `consecutive_holds`. Agent output adds a trusted `BACKLOG:` preamble. Held
responses create no Feed request or new exposure and are not cached as new
pulls. Three consecutive holds or a six-hour-old pending item force a new pull.
Owner requests, absent origin, `--action more`, V1 and default `off` always fetch.
A later `--origin owner` overrides the heartbeat prefix.

`settings sync` stores `feed_backlog_mode` per server. An absent or unknown value
resets it to `off`; it is never pushed back as a user setting. Existing polling
interval preferences and user-set flags are unchanged. The cohort setting must
be provided by the separate server rollout before holds can activate.

## Feedback delivery

`feed feedback` validates and normalizes items, preserves explicit impressions,
and infers missing impressions from the latest cached exposure. A deterministic
32-character SHA-256 key includes endpoint, Agent, impression, item, score and
the optional surface declaration. Retries reuse the key; changed judgments get
a new key. The wire request has batch `contract_version=feedback.v2` and per-item
`impression_source` when an impression is known. Omitted surface stays unknown.

Feedback is durably enqueued before sending, mixed with existing behavior events
but routed to the correct API. Ordered per-item results update the report ledger;
rejected items retain their reason in command output. Network failures preserve
the queue and return a queued summary. Every normal CLI invocation attempts a
bounded flush using the saved access token. Credential refresh remains with the
foreground authenticated command. Queue entries expire after 72 hours.

The trailing 24-hour health report includes pulls, delivered/scored items, status
runs, attempts/failures, queue drops and queue depth/age. Settings push includes
nonzero `feedback_health` independently of runtime identity changes. The local
backlog counters are sent only after settings advertise
`feedback_health_backlog_supported=true`; otherwise they are omitted for server
compatibility. No new server health columns or cohort rollout are enabled here.

## Skills and release boundary

`ef-broadcast` requires CLI 0.7.0. It scores backlog items before presenting,
respects `already_surfaced`, and runs `feed status` before leaving the Feed stage.
The completion step is disabled for incomplete onboarding. The existing
baseline no-scoring rule remains unchanged. Declaring `surfaced` during scoring
and changing the meaning of score zero remain a later Skill revision.

Publish the CLI before the compatible Skills bundle. The automatic Skills
workflow runs after merge, so coordinate CLI publication before promoting this
bundle to new installations. OpenClaw 0.0.48 and Claude Code 0.0.16 add plan and
capacity gates, retain failed deliveries for one retry, and mark scheduled pulls
only with a supported CLI. Codex requires no adapter change.

The server-delivered output contract and server capability catalog are unchanged.
Completion and backlog instructions live in the CLI 0.7.0-gated Skill and the new
CLI agent prompt. `feed status` is a local diagnostic and needs no server endpoint
or capability publication. Foreground feedback and explicit queue flush resolve
and refresh credentials; the bounded background retry uses saved credentials.
