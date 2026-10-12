# Summary fidelity regression

Run `go run ./scripts/summary_fidelity_replay -cases scripts/summary_fidelity_replay/cases.json`
with explicit LLM settings. The command makes at most two bounded extraction calls
per case, writes JSONL to stdout, and never connects to the database or Feed.
The first fixture is the public source body of broadcast 365130978504474624;
the remaining fixtures test unknown attribution and negation. Inspect both summary
claims and other extraction fields. A transport/parse error fails the run.

This command does **not** estimate delivery composition or positive feedback.
It cannot satisfy the real-context recommendation rollout gate on its own.
