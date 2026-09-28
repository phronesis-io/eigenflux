# Commission dispatch plan

Status: durable notification intake, seller input inspection and paid local fulfillment; acceptance semantics, upload/delivery and buyer receipts remain pending.

## Baseline

The Commission branch was rebased onto main `3a3612d1`; rebase repairs are at `8989581a`. Branch `codex/commission-outer-loop-20260928` merges the existing account watch at `94d59c67`, including Socket/HTTP reception diagnostics. The old local and remote Commission heads are retained as backup branches. No production deployment or forced remote update was performed.

Baseline CLI full tests and focused API capability/route tests pass. Existing automatic PM handling is available. Commission notifications enter a durable queue. A separate worker invokes a seller Agent to inspect frozen inputs and records its decision locally; paid `ready` results atomically queue a separate local fulfillment job.

`commission_order` intake validates the account and notification, persists before ACK and deduplicates order/version/role. It uses the existing Socket only alongside a PM subscription; Commission-only bindings use initial HTTP intake and 60-second reconciliation to avoid the server Socket marking PM read. The original stream drain respects that persisted ownership. These intake guarantees do not establish the 180-second business result or any user notification.

Intake validation: CLI `go test ./...`, focused Commission/binding/ownership race tests, and Darwin arm64 / Windows amd64 builds pass. Fixtures cover persistence failures, account switching, binding handoff, 100-item Socket batches, pagination and PM cursor isolation. Real Commission orders and model execution have not been tested.

## Implemented seller inspection

The worker validates the pinned seller, latest order and frozen fulfillment Skill, downloads authoritative inputs with byte-count/SHA-256 checks, and passes local files to a fresh Agent session. Signed `ef-commission/references/dispatch.md` owns reasoning. Store the matching result and inspected paths atomically; reject incomplete `ready`, changed orders and changed identity. Local downloads are temporary, capped at 128 files / 64 MiB. Completed job compaction retains results.

Input manifests use the existing Agent-authenticated GET `/api/v2/console/trade/orders/{id}`. The new exact GET Caddy route exposes this existing handler; other methods, lists and subpaths remain unchanged. Local route tests pass; it has not been deployed. Each input retains its own immutable snapshot ID. The model neither downloads files nor reads ambient account credentials.

The 180-second local ceiling starts after queue claim and includes preparation. It is not the requested buyer-visible end-to-end deadline. No lease capability or remote readiness receipt is advertised. CLI full tests, focused Commission/dispatch/Skills race tests, Darwin arm64 and Windows amd64 builds pass. Caddy route tests pass. Fixtures use temporary accounts, signed rules, HTTP servers and mocked runners; they do not establish real-host or production acceptance.

## Implemented paid local fulfillment

A separate serial worker rechecks `in_progress`, identity, version, frozen Skill and verified materials before executing. It creates a private output directory inside WorkDir and persists that path before invoking the host. The model generates every contractual artifact and reports self-check evidence. CLI checks actual regular files, paths, SHA-256 and size; it retains outputs and partial work. Order changes and identity switches cancel execution through independent monitors. New intake and PM work do not wait for local fulfillment.

A structured `artifacts_ready` result means local generation only and remains `needs_user` for submission authorization. It neither uploads nor delivers. Scope-specific external operations, paid subcontracting and payments remain separate. Triggering is notification-driven; it does not scan historical orders. Deduplication uses retained order-scoped journal records, not a permanent execution ledger.

Fixture acceptance covers paid intake → queue → artifact generation/evidence, unpaid/stale orders, account switches, order cancellation, blocked-network identity checks, partial output retention and recovery. CLI full tests, related race checks and Darwin arm64 / Windows amd64 builds pass. An opt-in native Codex test passed on 2026-09-28: actual model inspection 35.4s, actual file generation/self-check 83.9s; the exact 11-byte artifact and SHA-256 matched. Order APIs and accounts were fixtures, not production orders. ACP Agents must provide their own usable file tools.

## Product decision

Current Commission semantics automatically accept each order when created. `order accept` is retired. Publishing a service authorizes platform acceptance, not a claim that the seller Agent inspected the order. Preserve this state machine unless the owner explicitly chooses Agent-gated acceptance; that alternative requires changes to the independent Commission service and payment flow.

The proposed 180-second result is a seller intake receipt: `ready`, `needs_input`, `needs_user`, `failed`, or `timeout`. A ready receipt proves the seller Agent checked the frozen contract, fulfillment Skill and actual materials. It does not prove payment, completed work or delivery. The product choice is pending owner confirmation.

## Smallest implementation

1. Reuse the existing authenticated PM WebSocket `notification_push` and `/notifications/pending` fallback. Persist a pinned account/order/version job before notification ACK. Prevent stream/heartbeat from consuming notifications owned by watch. Re-read order role, identity, state, contract and Skill binding before invoking the Agent.
2. Advertise Commission dispatch readiness through the existing runtime lease. Online is provisional; an actual receipt proves execution. Do not infer readiness from an ordinary socket connection.
3. Add an authenticated, order-scoped intake request/result API and a bounded buyer wait command. Use a server-issued deadline 180 seconds from request acceptance, including queueing. Return an explicit timeout by the deadline; never fabricate rejection or success. Authorize each read/write against the authoritative order parties and frozen contract/input manifest; reject cross-order/cross-account and late success writes. A payment-only version change must not invalidate unchanged inputs.
4. Keep short intake work separate from long fulfillment so long work cannot block new intake. Skills own reasoning; CLI owns deadlines, identity, persistence and receipts. Do not use PM content to select executable commands.
5. Trigger fulfillment only after authoritative `in_progress`, verified materials and the frozen fulfillment Skill. Produce and validate contracted artifacts. Upload/deliver only under explicit authorization covering the action and scope. Unknown side effects require readback and human reconciliation, not blind replay.
6. Store user-facing results durably and separately from compacted job bodies. A heartbeat report stage reads unreported results, groups ordinary PM/Commission updates, and acknowledges only after the host presents the report. Keep needs-user/failure actionable. Immediate notifications require an actual host sink; stdout alone is insufficient.

## Automation boundary

Automatic: new-order inspection, Skill checks, payment tracking, authorized fulfillment, artifact self-checks, validation/delivery/settlement progress observation and reconnection reconciliation.

Scoped authorization: artifact uploads and delivery submission. Seller publication is not blanket authorization for arbitrary external tools or additional spending.

Explicit user action: new paid subcontracting, payment, payout/KYC/withdrawal and any unsupported refund/cancellation decision. Current automatic-accept orders cannot be rejected through the old awaiting-seller-only action. Missing materials do not revive retired submit-materials or change the frozen contract.

## Existing visibility and PM summary

Native dispatch creates an independent Codex/Claude/OpenClaw session; it does not inject work into the user's active conversation. Hosts may retain or display that independent session. Custom ACP/command UI behavior is host-defined.

There is no current Feed summary integration. The strict PM decision contains no summary; journal completion stores status/session/reply IDs, then removes completed bodies. Watch-managed heartbeat skips Communication, and Feed poll does not read the journal. `needs_user` and failures currently reach logs only. This needs a result/report path; Skill instructions alone cannot provide it.

## Acceptance

- Real seller Agent reads frozen contract/materials and emits a matching intake result within the total deadline; offline, busy, missing Skill, invalid materials and timeout are explicit outcomes.
- Socket, HTTP fallback and restart duplicates create one intake/fulfillment; failed persistence never ACKs.
- Wrong role/account/version cannot execute or receive a receipt. Terminal order changes stop stale execution.
- Unpaid orders never start fulfillment; long fulfillment does not block intake.
- Authorized artifacts are uploaded, downloaded and verified before delivery; unknown effects do not repeat automatically.
- Background PM/Commission results remain pending until user-visible reporting is acknowledged; no lost or repeated summaries across restart or rebind.
- Existing PM, account isolation, heartbeat ownership and Mac/Windows behavior continue to pass. Real-host acceptance remains separate from fixtures.
