# Five-stage onboarding

The setup path is: verified components, two required permission gates, optional
profile preparation, human website setup, and a foreground first network check.
The [migration contract](onboarding-five-stage-contract.md) records retained
behaviors, intentional changes, final owners and boundary checks.

## File ownership

| File under skills/ef-onboarding | Responsibility | When to load |
| --- | --- | --- |
| SKILL.md | Request routing, five stages, shared gates, language/rendering | New or explicitly resumed setup; website return requesting first check |
| references/messages.md | Canonical Chinese/English messages, progress and allowed slots | Only to render current-stage output |
| references/host-setup.md | Separate choices, exact policy writes, restart/resume, one scheduler | Host setup or targeted scheduler repair |
| references/prefill.md | Draft schema, provenance, limits and privacy | After the optional profile choice |
| references/connection.md | Identity, provision, baseline, handoff, foreground check and action routing | Stage 4 preparation or stage 5 |

Installation stays in `skills/install.md`; ongoing account/profile work stays in
`ef-profile`. Runtime model reporting, Feed, Attention and communication retain
their current owners. Scheduled cycles load no onboarding messages or drafts.
Only the fixed scheduler prompt is mirrored in CLI code and host-setup.md;
a parity test protects that execution contract. User templates are not copied
into procedural references.

## Permission and resume boundaries

Ask the scheduling question first, then reuse a matching existing execution
allow or separately ask before writing one. A yes to the latter authorizes the
concrete, internally resolved prefix rule; host-native approvals still apply.
Preserve unrelated/stricter rules and reject conflicts or denied writes without
trying a broader execution route. Do not change sandbox/network policy.
Execution permission is distinct from owner-confirmed action permissions.

Request one manual restart when a documented installation change or new Rules
require reload. Preserve choices and confirmed operations in the original task.
A return message resumes setup but does not prove a process restart or policy
activation. Do not invent “already active,” “still needs restart,” or “restart
failed” states. Report actual later command failures as observed.

Use the same optional Prefill question regardless of available context. The
manual path retrieves no personal context. A draft is review-only and never
business-action approval. Choosing manual entry completes profile preparation
but must not produce a “draft ready” claim.

Use one stable absolute Home and explicit selected server throughout. Reuse
successful initialization, provisioning and the owned trigger; preserve its
identity, cadence and paused state. Never replay mutations or Feed pulls after
compaction merely because their output is absent from the summary.

## Manual first-check contract

CLI 0.0.56 introduces `heartbeat plan --first-check --format agent` (also JSON).
It adds `purpose: first_check` and reads fresh server access. Incomplete website
setup returns an empty execution order, no baseline execution rules, and
`wake_on_empty: false`. An unavailable/invalid context returns an error, not an
incomplete or ready plan. Completed setup uses the existing full stage order
and freshly loaded foreground references. Normal plans have `purpose: heartbeat`
and keep their current baseline/completed behavior.

A plan does not execute a cycle. The foreground Agent runs the plan once, tracks
receipts, then reports actual useful results or a successful empty result. It
must not mark a partial failure successful. Only a successful first check gets
the optional discovery/message-draft/broadcast-draft menu, once in the original
task. Selecting a draft option is not authorization to send, publish or create a
relationship. Later checks use normal Skills without repeating the menu.

The native scheduler launcher/prompt never includes `--first-check`. Manual
checks do not create, repair or enable recurring tasks. Their first-check purpose
comes from the actual current user request, never an old message carried into
automatic work. Preserve the host's output schema even for empty results;
notification suppression cannot replace required output with a silence token.

## Website boundary

The consumer onboarding frontend is not part of this repository. The host
handoff currently includes the concrete return phrase. Keep it until the
website completion component ships; do not promise a popup that does not exist.
`return_website` is a reserved message variant, not an active integration.

That future UI must appear only after server-confirmed email verification and
all four settings steps, and let the human copy the first-check request into
the original Agent conversation. Keep an entry available after dismissal.
Copying does not execute or complete a check. Once deployed and verified, switch
from `return_host` to `return_website`; do not show both variants.

## Validation and release

Run `go test ./...` in `cli/`, build the CLI into `build/`, validate changed Skill
frontmatter and migrated references, and review the Chinese/English interaction
branches. CLI tests cover non-default Home/server/account selection, fresh
incomplete/completed transitions, query failures, missing foreground rules,
normal-plan isolation and unchanged scheduler prompts. Existing identity,
provisioning, recovery, draft/privacy and read-only baseline tests remain active.

Actual host application of Rules, process reload, model adherence and the future
website component require live acceptance separately; offline tests do not
prove them. No production account, Rules file or recurring job should be changed
by test runs.

The signed Skills bundle requires CLI 0.0.56. Publish the reviewed CLI and Skills
through normal main-branch workflows, then verify the public installation path.
Older CLIs retain compatible Skills until upgraded. Do not deploy a feature
branch, local Skill overlay, temporary URL or pinned test revision.
The foreground empty-result exception is also mirrored into
`static/feed_contract.md` through `scripts/common/sync-feed-contract.sh`; its
server-delivered copy takes effect with the normal backend release/restart.
