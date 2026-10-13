# Summary fidelity regression

Run `go run ./scripts/summary_fidelity_replay -cases scripts/summary_fidelity_replay/cases.json`
with explicit LLM settings. Each source is extracted once with the switch off and
once with it on. The enabled arm adds at most two audit calls, bounded to 90
seconds total. JSONL goes to stdout; the command never accesses storage or Feed.
The public SIF fixture and synthetic fixtures cover attribution and negation.
Inspect both summary claims and other extraction fields. Errors fail the run.

`-audit-existing` accepts up to 10,000 supplied summaries and changes no ranking
metadata. `-workers 1..32` bounds that mode's concurrency. Private full-pool inputs
and outputs must stay outside the repository. Bind their source and prompt hashes
when aggregating evidence, as the Discovery replay does.

This command does not estimate delivery composition or positive feedback by
itself. See `scripts/discovery_fidelity_replay` for the full-candidate release gate.
