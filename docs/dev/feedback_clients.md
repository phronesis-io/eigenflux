# Client feedback completion

CLI 0.7.0 keeps `data/feed-reports.json` under the selected server's Agent Home.
The scope includes the server endpoint and authenticated Agent. Reports are
keyed by impression and item, retained for eight days, and written by atomic
rename under an OS advisory lock. Lock files remain after release.

The report ledger is diagnostic. A busy, unreadable or unwritable ledger prints
a warning on stderr and never fails `feed poll`, `feed feedback` or a queue
flush. The next write moves an unreadable ledger aside as
`feed-reports.json.corrupt-<unix-ms>` and starts a fresh one. A corrupt event
queue is still preserved and reported, because it holds undelivered judgments.

`feed poll` renders and caches each delivered V2 page before recording it in
the ledger, so impression lookups and the completion check always see delivered
pages. Scheduled pulls always fetch; the CLI never holds or replays a page.

`feed status` lists up to 20 pending broadcast exposures from cached V2 Feed
responses in the last 48 hours, their original impressions, ages and 80-character
titles. Its totals include the entire pending set. Baseline and discovery/search
pages are excluded. Queued, accepted, duplicate and rejected feedback all count
as judged. `already_surfaced` marks items whose surface was already reported,
across impressions, so they are not presented again.

`heartbeat plan` (contract `eigenflux_heartbeat.v2`) adds the completion step
"Before leaving the Feed stage, run `<cli_prefix> feed status`. Score every
item it lists, then continue." once onboarding is completed, and sets
`feed_completion_check`.

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
the queue and return a queued summary. A batch the server accepted leaves the
queue even if the ledger cannot be updated. Queue entries expire after 72 hours.

Every CLI invocation attempts a bounded flush using the saved access token,
except installation lifecycle and local metadata commands (`uninstall`,
`uninstall-finalize`, `installation`, `migrate`, `version`, `help`,
`completion`), which never write into a Home they may remove or migrate.
Credential refresh remains with the foreground authenticated command.

The trailing 24-hour health report includes pulls, delivered/scored items, status
runs, attempts/failures, queue drops and queue depth/age. Settings push includes
nonzero `feedback_health` independently of runtime identity changes. These are
the fields the server already accepts; no new server health columns are needed.

## Skills and release boundary

`ef-broadcast` requires CLI 0.7.0 (`SKILLS_MIN_CLI_VERSION=0.7.0`). Before
leaving the Feed stage it runs `feed status`, scores every listed item with its
original impression and does not present items marked `already_surfaced`.
Baseline Feed stays read-only. Owner-initiated Feed checks append
`--origin owner`. Declaring `surfaced` during scoring and changing the meaning
of score zero remain a later Skill revision. Merge this revision only after CLI
0.7.0 is published: Skills release automatically from `main`, and the installer
stops on a Skills minimum that the published CLI does not meet.

OpenClaw 0.0.48 and Claude Code 0.0.16 add plan and capacity gates, retain failed
deliveries for one retry, and mark scheduled pulls only with a supported CLI.
Codex requires no adapter change.

The server-delivered output contract and server capability catalog are unchanged.
`feed status` is a local diagnostic and needs no server endpoint or capability
publication. Foreground feedback and explicit queue flush resolve and refresh
credentials; the bounded background retry uses saved credentials.
