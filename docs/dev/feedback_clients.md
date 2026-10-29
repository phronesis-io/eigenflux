# Client feedback completion

CLI 0.7.2 keeps `data/feed-reports.json` under the selected server's Agent Home.
The scope includes the server endpoint and authenticated Agent. Reports are
keyed by impression and item, retained for eight days, and written by atomic
rename under an OS advisory lock. Lock files remain after release.

The report ledger is diagnostic. A busy, unreadable or unwritable ledger prints
a warning on stderr and never fails `feed poll`, `feed feedback` or a queue
flush. The next write moves an unreadable ledger aside as
`feed-reports.json.corrupt-<unix-ms>` and starts a fresh one. A corrupt event
queue is still preserved and reported, because it holds undelivered judgments.

Without `--batch`, `feed poll` preserves whole-page output and always fetches.
V1 ignores batching; baseline pages are always whole and read-only.

`feed poll --batch N` (1–20) saves a V2 page before printing its first N items.
Later polls serve open items and backlog locally, oldest page and page position
first, while still synchronizing control context, runtime heartbeat and settings.
`--action more` and `--origin owner` always fetch; an owner check continues
with saved work through `feed next`. With work remaining, a scheduled poll
fetches a new page only after two consecutive local polls and at least 30
minutes since the last fetch, to bound notification delay. Otherwise the saved
work is processed first.

`feed next --batch N` defaults to 5 and reads only local state, including skipping
the background event flush. It selects up to N items from one page under the
report lock. Open items (batch-handed, unscored, unsurfaced, handed fewer than three
times) precede never-handed backlog. Items served more than 24 hours ago are
excluded. Old ledger entries and whole pages count as handed
at `served_at` for pending status and feedback attribution. Only items marked
`batched=true` by a batch handout can be re-handed. Entries from CLI 0.7.0 or
0.7.1 lack this marker and are never re-handed after upgrading; explicitly
unhanded backlog remains eligible. Whole-page delivery leaves `handed_at`
absent and does not set `batched`.

Cache or ledger write failure prints the fetched whole page with a stderr
warning. Local continuation errors preserve the saved page and report failure.
Batch output keeps the page envelope and adds `local_batch`, including source,
original impression, handed count, remaining open/backlog counts and a command
preserving Home, server and agent format when selected. Each item carries
`already_surfaced`. Cards include only the batch's authors; `discovery.items`
contains only its items in batch order. A discovery preview is omitted when each
of its fields equals the item preview's (discovery previews carry `text`; Feed
previews add `truncated`). Other discovery fields and the cached page remain intact.
Local batches use the current
applied control context and empty notifications, retaining the original context
revision in the report ledger.

Agent-format batches (`feed poll --batch N --format agent` and
`feed next --batch N --format agent`) print compact JSON and trusted notices
outside the untrusted payload. An action's instruction becomes `instruction_ref`
only when its text exactly matches a currently printed intent's `then` and that
intent is in the item's `matched_intent_ids`. Changed or unmatched instructions
remain inline; the trusted line explains how to resolve references.

Each agent-format batch writes its served output contract by atomic rename to
`data/feed-contracts/<sha256[:16]>.md` under the selected server. The trusted
notice gives its absolute path, hash and byte count. Read the file completely
before processing the batch, once per cycle and again when the hash changes.
Writes prune contracts older than eight days. A failed file write prints the
contract inline. JSON, baseline, whole-page and fallback outputs retain their
inline contract and instruction text.

`feed status` lists up to 20 pending broadcast exposures from cached V2 Feed
responses in the last 48 hours, their original impressions, ages and 80-character
titles. Its totals include the entire pending set. Baseline and discovery/search
pages and never-handed backlog are excluded. Backlog is counted separately.
Queued, accepted, duplicate and rejected feedback all count
as judged. `already_surfaced` marks items whose surface was already reported,
across impressions, so they are not presented again. It reads the ledger without
taking its lock; a busy ledger only skips the `status_runs` counter.

`feed status --after-push` lists only positive scores (1 or 2) from the last two
hours that have no surface report. It records each listed exposure as reminded
under the report lock, so each is listed once. With a busy ledger it still lists
them without recording the reminder. The plain `feed status` never lists or
consumes these reminders. Report surface only for items actually shown; scoring
never declares surface.

`heartbeat plan` (contract `eigenflux_heartbeat.v4`) requires reading the batch's
contract file completely before processing it. It adds the completion step
"Before leaving the Feed stage, run `<cli_prefix> feed status`. Score every
item it lists, then continue." once onboarding is completed, and sets
`feed_completion_check`. After the push, it requires `feed status --after-push`
and surface reporting only for listed items the owner was actually shown.

## Feedback delivery

`feed feedback` validates and normalizes items, preserves explicit impressions,
and infers missing impressions from the most recently handed exposure; equal hand
times, such as responses cached in the same millisecond, resolve to the newest
cached response. A deterministic
32-character SHA-256 key includes endpoint, Agent, impression, item, score and
the optional surface declaration. Retries reuse the key; changed judgments get
a new key. The wire request has batch `contract_version=feedback.v2` and per-item
`impression_source` when an impression is known. Omitted surface stays unknown.

Feedback is durably enqueued before sending, mixed with existing behavior events
but routed to the correct API. Ordered per-item results update the report ledger;
rejected items retain their reason in command output. Network failures preserve
the queue and return a queued summary. A batch the server accepted leaves the
queue even if the ledger cannot be updated. Queue entries expire after 72 hours.

Every CLI invocation attempts a bounded flush using the saved access token,
except installation lifecycle and local metadata commands (`uninstall`,
`uninstall-finalize`, `installation`, `migrate`, `version`, `help`,
`completion`), which never write into a Home they may remove or migrate, and
the local-only `feed next` command.
Credential refresh remains with the foreground authenticated command.

The trailing 24-hour health report includes pulls, delivered/scored items, status
runs, attempts/failures, queue drops and queue depth/age. Settings push includes
nonzero `feedback_health` independently of runtime identity changes. These are
the fields the server already accepts; no new server health columns are needed.
`feed status --format json` additionally exposes local counters `batches_handed`,
`local_polls`, `rehanded_items`, `backlog_expired_items` and `surface_reminders`
under `health`. These counters never enter the server's `feedback_health`.

## Skills and release boundary

`ef-broadcast` 0.14.34 requires CLI 0.7.1 (`SKILLS_MIN_CLI_VERSION=0.7.1`). Before
leaving the Feed stage it runs `feed status`, scores every listed item with its
original impression and does not present items marked `already_surfaced`.
Baseline Feed stays read-only. Owner-initiated Feed checks append
`--origin owner`, which fetches a fresh page, and then drain saved batches. Scheduled cycles process the pulled
batch of five and at most one additional batch. Combine recommendations into
one push per cycle; never carry scored items into later pushes. Surface remains
a separate report after delivery. Merge the Skill revision only after CLI
0.7.1 is published: Skills release automatically from `main`, and the installer
stops on a Skills minimum that the published CLI does not meet.

OpenClaw 0.0.49 and Claude Code 0.0.17 pass `--batch 5` only for CLI >= 0.7.1.
Existing plan, capacity, retry and delivery-timeout gates stay unchanged.
Codex requires no adapter change.

The server-delivered output contract and server capability catalog are unchanged.
`feed status` and `feed next` are local commands and need no server endpoint or capability
publication. Foreground feedback and explicit queue flush resolve and refresh
credentials; the bounded background retry uses saved credentials.
