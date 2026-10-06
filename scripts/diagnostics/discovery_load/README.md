# Isolated broadcast recall load comparison

This runner accepts only a plain HTTP loopback endpoint without credentials. It
uses its own Elasticsearch client, bypasses proxies, disables retries and never
reads production settings. Run against a disposable local ES; synthetic fixture
creation writes only `discovery-load-*`. The tool intentionally has no cleanup or
arbitrary-index option. Remove its owned container after the experiment.

Build artifacts belong under `build/`:

```sh
go build -o build/discovery-load ./scripts/diagnostics/discovery_load
build/discovery-load --url http://127.0.0.1:19201 \
  --seed-docs 2000000 --indices 30 --requests 240 --concurrency 1 --variant expiry
build/discovery-load --url http://127.0.0.1:19201 \
  --requests 120 --concurrency 8 --variant separate_recent
build/discovery-load --url http://127.0.0.1:19201 \
  --requests 120 --concurrency 8 --variant bounded_recent
```

Use a literal loopback bind, two CPU cores and a 512 MiB heap for the published
comparison. `--seed-docs` creates 30 single-primary indices with no replicas by
default; omit the flag on later runs. One third of fixtures are expired; older
matches deliberately have stronger keyword evidence than recent matches. The
five query terms include a broad `agents` term. Owners vary between requests.
There are no real profiles, vectors, user text or external model calls.

Every synthetic recommendation executes five broadcast contexts with the
Engine's six-call per-request concurrency bound. `original` removes only the
new expiry predicate. `expiry` uses the new expiry predicate. `separate_recent`
adds the recent query without shared admission, for comparison with the initial
implementation. `bounded_recent` invokes production `Source.Recall` for the recent
lane, including its shared concurrency/deadline controls. The bounded variant also
models Engine recent admission: one recent call per request before acquiring a
general recall slot, with the same one-second total budget. Ordinary queries share
the same raw HTTP/JSON reader across variants; production recent decoding adds
its normal Document/fingerprint work. This is a recall experiment, not a complete
Engine/HTTP benchmark.

JSON output includes recommendation P50/P95/P99, query count, peak client requests
until response headers, ES CPU/GC deltas, shard queries, rejection deltas and
sampled search queue/heap maxima. Each run warms the five ordinary terms first.
Use repeated runs in alternating order; short first-run results include JVM/cache
warm-up. Queue/heap samples every 100 ms can miss brief spikes. ES CPU is process
CPU time across cores and includes the sampler itself; it is not a CPU percentage.

Limits: at most two million fixture documents, 30 indices, 500 recommendations
and 32 simultaneous recommendations per invocation. The dataset omits embeddings
and does not represent production document size or traffic. Closed-loop bursts establish relative query
cost and exercised queue behavior, not a production arrival-rate SLO, a maximum
capacity claim, dense retrieval cost or end-to-end latency. No load is sent to
production or staging.
