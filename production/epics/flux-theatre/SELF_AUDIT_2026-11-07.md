# Pre-review self-audit — Flux Theatre PR (2026-11-07)

- **Scope**: the 103-file, 7-commit PR (`pr/flux-theatre-s1`) — `hub/`, `fluxspace_client/`, `docs/architecture/`, `production/epics/`
- **Method**: mechanical checks first (route inventory vs contracts, per-package test counts
  vs documented counts, stale-number grep, TODO/error-discard grep, wall-clock vs injected
  clock), then a read of the state machine, the SSE loop and the governance write path
- **Why**: a public review finds drift faster than an author does. Four issues were real and
  are fixed below; six are recorded as open with the reason each was not fixed

> **Resolution update (same day, after ADR-0009 was signed).** Findings 5 and 6 are
> closed by ADR-0009 and implemented: `/events` now requires the seat token issued
> at join, and governance routes resolve a companion→owner registry before any
> decision. The residue of finding 6 is closed too: live-match reservation moved
> from creation to join, so a lobby holds no slot for an owner who never showed
> up.
> The table below is otherwise left as written: an audit is a snapshot, not a
> document that improves retrospectively.

## Fixed in this pass

| # | Severity | Finding | Evidence | Fix |
|---|---|---|---|---|
| 1 | **Medium** | A documented guarantee was false: "all eleven governance endpoints answer `503 GOVERNANCE_UNCONFIGURED`". The model-consistency route never consults governance state, so it answered normally. The doc claimed a fail-closed rule the code did not implement. | `api/governance.go` `archiveModelConsistency` calls `s.archives()`, not `s.governance()`; the claim was in contracts §11 and ADR-0007 §1 | Documented the exception instead of adding a pointless gate: the route is pure, so refusing it would be a rule with no reason. Contracts §11, ADR-0007 §1 and the package comment updated |
| 2 | **Medium** | No request-body size limit. `json.NewDecoder(r.Body)` will read until the client stops writing, so one POST could allocate without bound before any validation ran. | `api/sessions.go` `decodeBody` | `http.MaxBytesReader(nil, …, 1 MiB)`; `TestOversizedBodyIsRejected` asserts 400 `INVALID_BODY` rather than a crash |
| 3 | **Low** | The spectator token — the only credential the Hub accepts — was compared with `!=`, which leaks the secret's prefix to anyone who can time responses. | `api/sessions.go` `spectatorEvents` | `subtle.ConstantTimeCompare` |
| 4 | **Low** | `/snapshot` reported `time.Now()` instead of the injected server clock, so the one endpoint that stamps a response bypassed the clock every other path uses. A test with a fixed clock would have passed for the wrong reason. | `api/sessions.go` `getSnapshot` | Uses `s.now()`; `TestSnapshotEndpoint` now asserts the injected value, not merely "non-zero" |

## Open, with the reason each was left alone

| # | Severity | Finding | Why not fixed here |
|---|---|---|---|
| 5 | **High (known)** | **Participant stream auth is `?seat_id=` and nothing else.** Anyone who knows a session id and a seat id can read that seat's subjective stream, including its private decisions. | Needs an identity provider, not a code tweak. It is already stated in ADR-0007 ("anyone who can reach the Hub can set a permission tier") and contracts §3 describes the mechanism honestly, so this is a declared gap rather than drift. Fixing it properly is the Hub-auth work item, and that item needs an owner decision first (see below) |
| 6 | **High (design)** | **The Hub has no companion→owner table.** Governance isolation is by `companion_id`, which is isolation *between companions*, not *against a caller claiming someone else's companion id*. Every governance endpoint trusts the id in the path. | This is the finding that changes a design rather than a line. Authenticating callers without an ownership registry yields a check that looks stronger than it is, so it needs its own ADR before an implementation. Recorded as the top input to the Hub-auth slice |
| 7 | **Low** | `archive/views.go` ignores three `json.Unmarshal` errors when deriving key moments. A malformed payload silently produces a moment with magnitude 0 instead of failing. | Payloads are written by the Hub itself and the archive is hash-verified before any view is built, so the reachable failure is a programming error, not client input. Returning an error would change a projection API for a case that cannot occur from outside |
| 8 | **Low** | `BudgetLedger.Decide` (pre-flight) and `Registry.Approvals` (diagnostics) have no production caller — both are exercised only by tests. | They are the intended seams for the client-side pre-flight check and the operator console. Deleting them would remove a deliberate API; leaving them undocumented as unused would be the mistake, so they are listed here |
| 9 | **Low** | Oversized bodies return `400 INVALID_BODY` rather than `413`. | `MaxBytesReader` can only signal through the `ResponseWriter` it was given, and `decodeBody` has no writer. A 400 with a clear message is honest; changing the signature to thread `w` through ~20 call sites is churn for a status code |
| 10 | **Info** | The audit chain is tamper-*evident*, not tamper-*proof*: a writer with filesystem access can rebuild the whole chain and the snapshot consistently. | Stated in ADR-0007 and ADR-0008 rather than hidden; closing it needs signing and key management the Hub does not have |

## Consistency checks that came back clean

| Check | Result |
|---|---|
| Routes registered vs routes documented | **40 registered, 39 documented under `/hub`, plus `/health`** — 0 phantom endpoints, 0 undocumented ones |
| Documented per-area counts (9 session / 6 negotiation / 2 follow-up / 11 archive / 11 governance) | all correct against `Register*` |
| Total test count vs the claim in the plan | **129** — matches (api 21, archive 15, followup 8, generator 19, govern 21, invitation 7, models 2, referee 6, services 18, session 12) |
| Per-slice claims (archive "15 tests + 3 HTTP", governance "21 + 7 HTTP") | both correct |
| Stale number sweep ("118 tests", "78 Go tests", "12 tests", "5 HTTP tests") | no hits left after the ADR-0008 pass |
| ADR inventory | 8 files, ADR-0001…0008, contiguous |
| `gofmt` / `go build` / `go vet` / `go test` | clean / pass / pass / **130 pass** after the two new tests |
| Balance gate | PASS — 2,400 matches, 10.54% vs 0.08 floor |

## What this audit did not verify

- **Anything requiring the live stack.** S1's invitation round-trip through the real
  EigenFlux gateway is still unproven (no WSL 2 / Docker daemon), so "code complete" remains
  the honest status. Nothing in this pass touched that gap.
- **Concurrency under load.** `-race` needs gcc, which this machine does not have. The locks
  were read, not exercised: `session.Manager` (one mutex per manager), `govern.*` (one mutex
  per component), `archive.Store` (one mutex, plus `Publish` reading a pointer outside the
  lock before re-taking it).
- **Flutter side.** `flutter analyze` / `flutter test` were not re-run in this pass; the
  client was not changed.
- **The arbitration between `views.go` and `followup` projections and the GDD's exact
  wording.** Spot-checked, not read line by line.

## The two decisions this audit hands back

1. **Hub auth model** — who a caller is (shared secret per owner, signed token, or delegated
   EigenFlux agent token), and whether the spectator token stays a separate mechanism.
2. **Companion→owner registry** — whether the Hub records which owner a companion id belongs
   to. Without it, finding 6 cannot be closed no matter how strong the authentication is, and
   ADR-0007's "per-companion permission isolation" keeps its narrower meaning.
