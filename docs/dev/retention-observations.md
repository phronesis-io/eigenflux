# Daily retention observations

Insights retains UTC+8 registration-day D1 activity (automatic Feed/feedback
included) and reports cycle execution separately. The product PRD lives in
`eigenflux-insights/docs/prd/retention-environment-analysis.md`.

A successful runtime settings report queues a daily environment snapshot. A
heartbeat plan queues a cycle start with its exact stage set and puts the cycle
ID and receipt instructions in both JSON/agent output. The persistent scheduler
prompt, scheduling frequency, business stages, content selection and notification
semantics do not change. No Skills update is required: only upgraded CLIs issue
the new commands. `heartbeat cycle-report --cycle ID --stage STAGE --result
completed|failed` records an account-scoped receipt; `--stage cycle` completion
requires all stages. The queue is bounded, locked and retained for seven days.
Stage commands only persist locally; the terminal cycle command attempts one
bounded upload, avoiding a network round trip after every business stage.
Runtime backlog is evicted before existing maintenance when queue capacity is
exceeded. Runtime uploads use isolated batches/backoff, so API rollback cannot poison
existing maintenance batches. Environment and plan observations retain the
identity captured before network work; an account switch discards the stale
observation. Environment deduplication uses UTC+8 dates.
Offline uploads retry through bounded maintenance backoff; a failed upload is
not a failed business stage. Old clients remain unobserved, not failed.

API uses the existing authenticated maintenance endpoint and 30-day table. No
migration or new database privilege is required. Deploy API before CLI release.
`ENABLE_RUNTIME_OBSERVATIONS=false` stops new collection, and
`EIGENFLUX_RUNTIME_OBSERVATIONS=0` disables CLI creation. Neither switch changes
business operations. API rollback precedes no schema deletion; old records stay.

Only OS and CPU architecture are measured hardware dimensions. Product/version
and model are reported, possibly absent. Do not infer physical model, RAM/GPU or
personal usage. Historical D0 environment must use D0 observations only. Current
settings are a separately labeled auxiliary view. Cycle completion is a client
assertion backed by matching start/stage receipts, not server proof of model
quality. Missing records cannot distinguish no task, pause, offline or old CLI.

Verification: runtime validator/HTTP identity tests, real loopback PostgreSQL
deduplication, queue concurrent stage/retry tests, CLI plan/metadata regressions,
Insights SQL fixtures, independent D1 aggregate reconciliation and browser tests.
Never write synthetic events to production to demonstrate collection.
