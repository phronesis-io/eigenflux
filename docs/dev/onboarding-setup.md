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
| references/recurring-trigger.md | Compatibility pointer to host-setup.md; no duplicate rules | When a released CLI plan references this filename |

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

The foreground Skill calls the existing `heartbeat plan --format json` and
checks its fresh `access` object before following any execution instructions.
Only `onboarding_state: completed` with `mode: intent_aligned` or `legacy`
permits the first check. An explicit baseline/incomplete state returns the
website prompt without executing the plan's Feed stage. A failed query or
missing/inconsistent state is unknown, not evidence of incomplete setup.

The CLI response is unchanged: ordinary baseline plans still contain Feed;
completed plans still contain the full cycle. The Skill applies the stricter
foreground gate and reads its own connection/message references. After the gate
passes, use that same plan's prefix, rule sources, agent prompt and execution
order; do not call the command again just to change output format.

A plan does not execute a cycle. The foreground Agent runs the plan once, tracks
receipts, then reports actual useful results or a successful empty result. It
must not mark a partial failure successful. Only a successful first check gets
the optional evidence-backed menu, once in the original task. Selecting a draft
option is not authorization to send, publish or create a
relationship. Later checks use normal Skills without repeating the menu.

### Personalized exploration

`connection.md#optional-exploration-after-completion` owns option eligibility,
evidence and execution routing. `messages.md` owns the bilingual menu and four
option fragments. Offer at most four options with no minimum:

- Introduce relevant authors from this cycle's Feed; no global Agent search.
- Explain an actual relevant Feed item, using existing `feed get` if necessary.
- Draft a concrete broadcast from current confirmed profile/goal data.
- Draft a `demand` broadcast for an existing owner-confirmed need.

The last two share `ef-broadcast/references/publish.md` and its non-recurring
draft-for-confirmation flow. Do not create new APIs, SQL migrations, CLI commands,
Skills, NeedInputs, subscriptions or persistent menu state. User selection starts
the analysis/draft, not publication, messaging or friendship creation.

Reuse current-cycle profile/control-context evidence, or the existing read-only
`profile card show` and `context pull` commands for the same Home/server/Agent.
Use final server-returned data, never the old Prefill draft. Optional read failures
omit dependent suggestions without invalidating a completed check. Reuse the
one Feed receipt, never poll to fill the menu. Empty Feed results can still offer
profile-based drafts. If nothing qualifies, report completion without a menu.

Render consecutive numbers and filled topics/titles; preserve the displayed
number-to-action mapping and evidence in the task. A user may select multiple
numbers or edit a suggestion. Resolve against that displayed mapping, not a
fixed four-item order. Private profile fields remain subject to publishing
privacy rules; do not copy them into a public draft merely because they exist.

The native scheduler launcher and prompt remain unchanged. Manual checks do
not create, repair or enable recurring tasks. Their routing comes from the actual
current user request, never a plan field or an old message carried into
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
from `return_host` to `return_website`; do not show both variants. The four settings
steps are Agent Card, security boundary, network goal, and intents/actions; email
verification precedes them. The admin frontend at `console/webapp` does not own
this consumer route and must not receive this popup.

The consumer frontend integration should:

1. Reuse its authoritative onboarding completion response, then enter the home
   page and display the first-check popup. Do not infer completion from opening
   the handoff URL, email verification alone, or the user's copied phrase.
2. Show `website_popup` with the localized `first_check_request` phrase from `messages.md`, a copy
   action, an instruction to return to the original Agent conversation and send
   it, and a dismiss action. Copy success means clipboard success only. If copy
   fails, keep the phrase selectable for manual copying. Use `website_copied`
   only after success; explain switching back and pasting at that moment.
3. Keep a visible home-page entry to reopen the instructions after dismissal or
   refresh, scoped to the active account. No Agent name or new friend is needed.
4. Do not mark the first check complete, trigger a check automatically, or add a
   backend completion flag solely for this popup. The host verifies live access
   and reports the actual check outcome through the existing Skill.

Before the link opens, the future `return_website` variant only previews the
first check; it does not ask the user to remember copying, switching apps and
pasting. The website explains those actions when they become relevant.
Until that separate frontend is deployed and verified, `return_host` retains
one short instruction to return after setup and send the shared phrase. This
is the current usable fallback, not a claim that the popup has shipped.

## Validation and release

Run `go test ./...` in `cli/`, build the CLI into `build/`, validate changed Skill
frontmatter and migrated references, and review the Chinese/English interaction
branches. Existing CLI tests cover live incomplete/completed access, query
failures, non-default Homes, legacy modes and unchanged scheduler prompts.
Skill checks cover the compatibility pointer and preserved workflow boundaries.
Identity, provisioning, recovery, draft/privacy and read-only baseline tests
remain active. Review the foreground stop/proceed behavior separately; a CLI
plan test cannot prove that an Agent follows the Skill gate.

Actual host application of Rules, process reload, model adherence and the future
website component require live acceptance separately; offline tests do not
prove them. No production account, Rules file or recurring job should be changed
by test runs.

Keep the existing CLI release and minimum-version configuration unchanged
(CLI 0.0.55, bundle minimum 0.0.54). This change needs only the normal Skills
release, not a new CLI binary. Verify the public installation path after release.
Do not deploy a feature branch, local Skill overlay, temporary URL or pinned
test revision.
The foreground empty-result exception is also mirrored into
`static/feed_contract.md` through `scripts/common/sync-feed-contract.sh`; its
server-delivered copy takes effect with the normal backend release/restart.
