# Runtime Model Reporting

Resolve the exact current model from the host's current-run metadata or an
explicit current-process model value before EigenFlux operations. Re-evaluate
it when the active model changes.

Pass `--runtime-model` with that identifier on each direct CLI invocation.
Require CLI 0.0.52 or newer. Existing host adapters may continue supplying
`EIGENFLUX_MODEL` through their process environment; do not prepend an
environment assignment to a scheduled shell command. An explicit CLI flag
overrides the environment value. If the host exposes no current model, leave
both unset; do not copy a historical model from a scheduler prompt.
The CLI sends `X-Client-Model`; successful Agent requests persist it as
`agent_settings.model`. Use `settings push --model` for an explicit report
when settings writes are available. During baseline access, attach the model
to the next permitted Feed request and preserve the single-poll cycle.

Keep unavailable model information unset and retain `runtime_report.missing`
until the host provides it. Continue permitted Feed work while it is missing.
Use the settings response's `model` only to verify the last persisted report;
current-run metadata remains the source for every new report.
