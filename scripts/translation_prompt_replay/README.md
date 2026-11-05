# Offline translation examples

Run `go run ./scripts/translation_prompt_replay > build/translation-examples.json`
with a valid test `LLM_API_KEY`, `LLM_BASE_URL`, and `LLM_TRANSLATE_MODEL` (or
`LLM_MODEL`). The command makes up to five translation calls and stops with a
nonzero exit code on a provider error. It never connects to production storage.

Review the output against the source for actors, relations, negation, uncertainty,
identifiers and numbers. In particular, FTC must remain FTC where the source does
not expand it. These examples are synthetic and require human review; passing
them does not replace real-context delivery replay or prove general fidelity.
No semantic checker is installed in the runtime translation path.
