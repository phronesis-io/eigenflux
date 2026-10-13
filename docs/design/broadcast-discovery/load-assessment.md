# Broadcast recall load assessment

Evidence collected on October 6, 2026. Production inspection was passive and
read-only; all synthetic traffic and fixtures ran on disposable local containers.
No service deployment or production write was performed.

## Production baseline

Observations at approximately 16:47–16:55 Asia/Shanghai:

- Recommendation traffic: about 16,315 requests over 24 hours; current five-minute
  rate 0.21/s, maximum five-minute average 0.42/s and maximum one-minute average
  1.2/s. These are averaged observations, not an instantaneous burst bound.
- Discovery recommendation histogram P95 was approximately 0.43 seconds over the
  recent 30 minutes; P99 over 24 hours was approximately 0.50 seconds. Histogram
  buckets provide estimates. No Discovery/channel errors were observed over 24 hours.
- Most requests used lexical fallback: current `no_active_needs` rate about
  0.19/s and `empty_context` rate about 0.013/s. This makes added keyword queries
  a material cost even without dense retrieval.
- Application host: 16 CPUs, 31 GiB RAM, about 26 GiB available, load average
  approximately 0.19. Sort's systemd memory observation was about 65 MiB.
- ES: six nodes including three data nodes, green health, 30 item primary shards
  and 1,888,852 item documents. Data-node snapshots showed 16 GiB JVM heaps,
  heap usage 35–56%, no active/queued searches, no search rejections and no breaker
  trips. These snapshots do not certify sustained future capacity.

The baseline offers no evidence that this change immediately requires more
servers. It also does not justify leaving the added work unbounded across requests.

## Added-work controls

The existing six-call semaphore belongs to **each request**, not the whole Sort
process. Five lexical-only contexts perform five ES searches before the recent
lane and ten afterward. Multiple requests multiply that work.

The final implementation adds:

- At most four recent searches sharing the service's `Source`, per Sort replica.
  More replicas multiply this bound; it is not a cluster-wide ES quota.
- At most one recent call per request. Waiting for this admission does not hold
  one of the six ordinary recall slots.
- A one-second deadline covering request admission, shared admission, search and
  response reading, plus an ES-side one-second search timeout. Parent cancellation
  propagates and deferred releases return capacity. Timeouts use the existing
  partial-retrieval diagnostics; they do not bypass relevance or eligibility gates.

ES timeout/cancellation are cooperative, not a guarantee that every shard stops
on the exact millisecond. Existing ordinary searches retain their prior controls.
This bounds the extra work rather than implementing a new global scheduler.

## Local comparison

ES 8.11, two CPU cores, 512 MiB JVM heap, 2 GiB container limit, 30 single-primary
indices, no replicas and 2,000,000 synthetic documents. One third were expired;
older matches had stronger keyword evidence. Every simulated request used five
broadcast contexts, including a broad `agents` keyword. The fixture has no vectors
or real user data. It approximates production document count/shard fan-out, not
production source size, embeddings, hardware, distribution or background traffic.

Each cell is the median of three runs, each with 240 simulated requests. Variant
order was reversed in round two; ordinary terms were warmed before each run.
The [per-run results](load-results.csv) contain all 27 runs (6,480 workloads).

| Concurrent requests | Expiry-only P95 / P99 (ms) | Recent without admission P95 / P99 (ms) | Final bounded recent P95 / P99 (ms) |
| --- | --- | --- | --- |
| 1 | 55 / 62 | 79 / 94 | 72 / 82 |
| 8 | 196 / 199 | 393 / 468 | 378 / 391 |
| 16 | 354 / 358 | 783 / 800 | 705 / 772 |

All runs completed with zero workload errors and zero ES search rejections.
At 16 concurrent requests, peak client searches until response headers were
80 / 96 / 84 respectively. Sampled search queue maxima were 396–397 / 418–455 /
399–404. Queue samples every 100 ms can miss brief spikes.

The bounded variant still executes ten searches per workload, versus five for
expiry-only. At 16 concurrency median ES process CPU time per workload was
approximately 36 / 86 / 79 ms respectively; CPU time includes the sampler and is
not CPU utilization. Admission reduced peak added work and P95 by about 10%
relative to the initial recent implementation. It did not remove its roughly
doubled CPU cost. The recent date predicate reduced shard execution: 36,000
queries per expiry-only run versus 42,000 with recent recall, despite twice the
client query count.

This is a closed-loop recall comparison. Ordinary queries use a common raw JSON
reader; bounded recent queries use production `Source.Recall` and model the Engine
admission gate. It excludes hydration, ranking, Feed/HTTP delivery and mixed
semantic traffic. Exercised throughput (roughly 22–25 workloads/s for bounded
recent at 16 concurrency) is not an arrival-rate SLO or a maximum capacity claim.
The [runner](../../../scripts/diagnostics/discovery_load/README.md) documents
parameters and reproducibility. Expiry prefiltering is limited to keyword queries;
dense query filters remain unchanged pending representative vector-load tests.

## Verification and rollout boundary

- Targeted Go race tests passed using isolated PostgreSQL 16, Redis 7 and ES 8.11
  for Discovery, delivery and the benchmark command.
- Cancellation/slow-search/full-admission regressions verify the shared four-call
  bound, capacity release, one-second waiting/search limits and ordinary-channel
  progress. Engine tests verify one recent call per request and visible partial
  failure with existing candidates preserved.
- Final Sort build and `go vet ./...` passed. Process E2E tests passed for Discovery,
  automatic recommendation HTTP and recall metrics against migrated local stores.
- The full root suite has not passed. An isolated local service run serialized
  packages (`-p=1`) and passed 90 package suites before legacy model-dependent
  cases blocked on provider authentication (HTTP 401); the run was stopped. A test
  helper port mismatch was corrected in test settings and its native ES case
  passed. `golangci-lint` 2.14.0 reports 514 existing findings on both main and this
  branch in uncapped reports, with no new semantic findings. Introduced-line lint
  is clean after handling response-close results in the fixtures/runner.
  Keep the PR draft until required repository-wide verification is complete.

The local findings support bounded evaluation of the keyword change at current
observed traffic, not an unrestricted rollout guarantee. Normal rollout must
observe Discovery latency/channel errors and ES search queues, rejections, CPU,
heap and GC against the baseline. Sustained queuing, new timeouts or a material
latency regression require reverting the Sort change or reassessing the lane.
Historical Reach repair was proposed separately in PR #369; that one-off tool
was never run in production and has since been removed.
