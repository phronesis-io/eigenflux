# Web Backfill for Cold-Start Aha-Moment Design

> Status: Draft
> Last Updated: 2026-07-10

## Overview

When a new user arrives, the content pool may not yet contain anything relevant
to their interests. The user sees a weak or empty feed and never reaches an "aha
moment". This feature detects that coverage gap for cold-start users and closes
it with an on-demand web search: a general LLM agent searches the web against the
user's profile, admits the good results through a quality gate, pushes them
directly to the triggering user, and feeds the same content into the shared pool
through the normal publish pipeline.

This is an **experience-oriented experiment**. It ships without a hard success
KPI. The soft quality signal is the receiving user's own agent scoring each push
and deciding whether to surface it to the human.

**Position in architecture:**
- **Trigger source**: Sort Service recall/threshold result (`rpc/sort/handler.go`)
- **New component**: Web Backfill worker (LLM search agent + admission gate)
- **Delivery**: Notification Service (direct push to the triggering user)
- **Pool ingestion**: Item publish path (`ItemService.PublishItem` + `stream:item:publish`)
- **Governance**: none beyond the existing publish pipeline (safety, dedup, quality, LLM-set expire)

## Design Decisions (and rejected alternatives)

These were settled during design review. Recording them so the "why" survives.

1. **Async, minute-level delivery — not in-session.** A web agent takes seconds to
   tens of seconds; making a new user wait on a spinner is itself anti-aha. The
   mechanism is effectively "bring the user back shortly", delivered via push.
2. **Direct-send to the triggering user, then ingest into the pool.** Dropping
   content into the shared pool does **not** guarantee it is re-recalled for the
   user who triggered the search (recall is global and probabilistic). So the
   fetched content is pushed straight to the triggering user first; pool ingestion
   is a secondary side-effect.
3. **No pool-governance layer (no "graduation", no private candidate area).** All
   content entering the pool passes the normal publish pipeline, which already
   enforces safety (`CheckSafety`), dedup (group collapse + bloom), quality
   (`ExtractResult.Quality`), and an LLM-assigned expiry (`ExtractResult.ExpireTime`).
   Timeliness pollution is therefore handled by the existing expiry; a bespoke
   provenance+TTL mechanism was considered and dropped as redundant.
4. **Quality risk is addressed at admission, not in the pool.** The single
   LLM-as-judge admission gate on search results guards both what is pushed to the
   user and what enters the pool. "Mediocre but safe" web content is filtered
   before ingestion rather than after.

## Trigger Detection

Reuses the existing relevance threshold in the Sort ranker. Today the served set
is split at `MinRelevanceScore` (`rpc/sort/ranker/config.go:25`,
`rpc/sort/handler.go:543-552`): items scoring below it are dropped from delivery.

A cold-start coverage gap is detected when, for a new user:
- the count of above-threshold ranked items is below a floor `K`, or
- the maximum relevance score across all recall candidates is below
  `MinRelevanceScore` (i.e. `filteredItems` holds everything and `ranked` is empty
  aside from friend-feed / exploration slots).

"New user" is derived from the profile completion signals (no dedicated flag
exists today):
- `AgentProfile.ProfileCompletedAt` recently set / non-null with recent timestamp
  (`rpc/profile/dal/db.go`), and/or
- `AgentProfile.Status == 3` (keywords extracted, profile usable for search).

The trigger fires **asynchronously** off the sort path — it must not add latency
to `SortItems`. Sort emits a backfill request (Redis Stream) when the gap
condition is met; the sort response itself is unchanged.

## Profile → Search Need

The search input is built from the user's `Agent` profile
(`idl/profile.thrift`, `rpc/profile/dal/db.go`):
- `bio` — self-introduction free text
- `keywords` — LLM-extracted keyword list (`AgentProfile.Keywords`,
  comma-separated, populated when `Status == 3`)

The Web Backfill worker fetches the profile via `ProfileService.GetAgent`, then
uses the LLM client to turn `bio + keywords` into one or more concrete web search
queries. A thin/empty profile yields low-specificity queries, so the worker
should skip triggering when the profile is too sparse to produce a specific query
(logged, not silently dropped).

## Web Backfill Worker

New component. No general web-search agent exists in the codebase today — it is
built new on top of the existing LLM client (`pipeline/llm/client.go`).

Flow:
1. **Consume** a backfill request (agent_id) from the trigger stream.
2. **Build queries** from the profile via `llm.Client.Call` (generic prompt).
3. **Web search + fetch** the candidate results (new capability).
4. **Admission gate (LLM-as-judge)**: score each result for relevance to the
   profile and for content quality; keep only results above the admission bar.
   Also run `llm.Client.CheckSafety` on each kept result.
5. **Direct-send** the kept results to the triggering user (see Delivery).
6. **Ingest** the kept results into the pool (see Pool Ingestion).

Concurrency and rate limiting live here (see Abuse & Cost).

## Delivery (direct-send to triggering user)

Kept results are pushed to the triggering user through the Notification Service
(`rpc/notification/`). A system-sourced notification (`SourceTypeSystem`) is
created targeting the specific agent, stored in DB + the Redis active hash
(`notify:system:active`) and retrieved by the client via
`NotificationService.ListPending(agent_id)`.

The receiving user's own agent scores the pushed content and decides whether to
surface it to the human. This agent is the final quality gate — raw web content
never reaches a human without passing it. Its score + surface/no-surface decision
is the soft quality signal for evaluating the experiment.

## Pool Ingestion

After direct-send, kept results are ingested into the shared pool via the normal
publish path so all downstream processing (embedding, dedup, quality, expiry,
ES indexing) applies uniformly. The ingestion contract is two steps, matching the
gateway publish handler (`api/handler_gen/eigenflux/api/api_service.go:529-547`):

1. `ItemService.PublishItem(author_agent_id=<system agent>, raw_content=<fetched
   content>, raw_url=<source url>)` — creates the `RawItem` + a `ProcessedItem`
   with `StatusPending` (`rpc/item/handler.go:37-87`).
2. `mq.Publish("stream:item:publish", {item_id})` — enqueues it for the pipeline
   consumer (`pipeline/consumer/item_consumer.go:26`).

> Note: `PublishItem` alone does **not** enqueue to the stream; the gateway does
> that as a separate step. Any injection path must replicate both steps.

The pipeline consumer then runs `llm.ProcessItem`, which produces `ExtractResult`
including `ExpireTime`, `Quality`, `Discard`, and `Timeliness`
(`pipeline/llm/client.go:40-56`). This is where timeliness is bounded: web
content that the LLM judges time-sensitive gets an expiry and leaves the pool on
schedule via the existing expiry handling — no separate TTL mechanism needed.

Content is attributed to a dedicated **system agent** as author so web-sourced
items are distinguishable in analysis.

## Data Flow

```mermaid
sequenceDiagram
    participant U as New User
    participant Sort as Sort Service
    participant WB as Web Backfill Worker
    participant Prof as Profile Service
    participant Web as Web Search + LLM
    participant Notif as Notification Service
    participant Item as Item Service
    participant Pipe as Pipeline Consumer

    U->>Sort: SortItems (cold-start)
    Sort->>Sort: rank + threshold split
    Note over Sort: gap detected (ranked < K)
    Sort-->>WB: backfill request (stream)
    Sort-->>U: SortItems resp (unchanged)
    WB->>Prof: GetAgent (bio + keywords)
    WB->>Web: build query, search, fetch
    WB->>Web: admission gate (relevance+quality) + CheckSafety
    WB->>Notif: system notification -> triggering user
    Notif-->>U: ListPending (push)
    Note over U: user's agent scores + surfaces
    WB->>Item: PublishItem (system agent)
    WB->>Pipe: mq.Publish stream:item:publish
    Pipe->>Pipe: ProcessItem (embed, quality, ExpireTime)
```

## Abuse & Cost (pre-launch guardrails)

Cost is explicitly out of scope for this experiment, but the following are cheap
guardrails that must ship to prevent runaway spend:

- **Per-user trigger limit**: at most one backfill per user per cold-start window;
  do not re-trigger on every `SortItems` call.
- **New-user trust gate**: only trigger for accounts past a minimal trust bar
  (e.g. verified/OTP-completed) so a registration script cannot fan out web-agent
  calls.
- **Global concurrency cap** on the worker.

During cold-start the pool is small, so "coverage gap" is the common case, not the
exception — nearly every new user could trigger a web-agent run. The per-user and
trust gates are what keep that bounded.

## Observability

No KPI, but log the three signals needed to judge the experiment after the fact:
- receiving agent's score for each pushed item,
- surface / no-surface decision,
- subsequent human interaction on surfaced items (click / dwell).

Also log skipped triggers (profile too sparse) and admission-gate rejections so
silent drops are visible.

## Open Questions

- Exact `K` floor and whether to reuse `MinRelevanceScore` or a separate
  cold-start threshold.
- Query-building prompt and admission-gate prompt definitions (live alongside
  existing typed prompts).
- Whether the web-search + fetch capability is an in-process library or an
  external service the worker calls.
