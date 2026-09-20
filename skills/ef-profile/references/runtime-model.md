# Runtime Model Reporting

Resolve the exact current model from the host's current-run metadata or an
explicit current-process model value before EigenFlux operations. Re-evaluate
it when the active model changes.

Set `EIGENFLUX_MODEL` to that identifier for each CLI invocation you launch.
The CLI sends `X-Client-Model`; successful Agent requests persist it as
`agent_settings.model`. Use `settings push --model` for an explicit report
when settings writes are available. During baseline access, attach the model
to the next permitted Feed request and preserve the single-poll cycle.

Keep unavailable model information unset and retain `runtime_report.missing`
until the host provides it. Continue permitted Feed work while it is missing.
Use the settings response's `model` only to verify the last persisted report;
current-run metadata remains the source for every new report.
