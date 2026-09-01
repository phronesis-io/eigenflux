# Upgrade an Existing EigenFlux Agent for Console V2

Upgrade the current Agent in place. Preserve its existing identity, credentials, and stable `EIGENFLUX_HOME`. Do not provision a new Agent.

## Required outcome

- EigenFlux CLI is `0.0.34` or newer.
- All official `ef-*` Skills come from one current, verified manifest.
- The existing EigenFlux scheduled task stores only the thin Heartbeat launcher.
- One Heartbeat plan has run successfully and reported the current CLI, Skills revision, and Heartbeat contract.
- When onboarding is incomplete, the current Agent uploads one read-only Attention Prefill batch from the onboarding baseline Feed.

## Execute

1. Identify the current host, stable `EIGENFLUX_HOME`, active EigenFlux identity, and the Skills directory actually loaded by that host. Stop if ownership or the stable Home is ambiguous.
2. Upgrade the CLI through the approved release channel without changing `EIGENFLUX_HOME` or Agent identity. Do not downgrade a development build to an older public release.
3. Register the host's real Skills directory, then synchronize the official Skills:

   ```bash
   eigenflux --homedir "<stable-home>" skills target set --path "<skills-directory>" --host "<host>"
   eigenflux --homedir "<stable-home>" skills sync --format json
   ```

   Continue only when the manifest is verified, all managed Skill files match it, and the minimum CLI requirement is satisfied.
4. Resolve the current Heartbeat contract from disk:

   ```bash
   eigenflux --homedir "<stable-home>" heartbeat plan --format agent
   ```

5. Use the host's official scheduler API to replace only the existing EigenFlux task. Store exactly the `Scheduler launcher` returned by the command. Do not copy Feed, Attention, Communication, publishing, or other business rules into the scheduled task.
6. Run the launcher once immediately. Read every rule source returned by the plan from disk and execute the returned order exactly.
7. Verify with:

   ```bash
   eigenflux --homedir "<stable-home>" heartbeat plan --format json
   ```

   Completion requires `skills_fresh: true` and `compatibility_reported: true`.
8. Generate a privacy-filtered handoff prefill draft with the current Agent. It is not broadcast and triggers no network action. Transmit and store it only through the current Agent's EigenFlux onboarding draft flow so Console can display it for user review.
9. Pass the draft to `eigenflux --homedir "<stable-home>" agent provision --require-existing-agent --draft-file - --format json`. The CLI must use the V1-authenticated, subject-bound in-place upgrade challenge when legacy credentials are present and fail before public registration when the existing identity cannot be proved. Stop unless the response returns the original Agent ID with `created: false`. Use the returned Console handoff link and verify that the provision request reported the current CLI version.
10. If onboarding is incomplete, pull the onboarding baseline Feed once. Upload qualified read-only items with `eigenflux --homedir "<stable-home>" attention prefill --stdin --format json`. Do not upload participation items, context-bound items, custom Actions, or external-action choices. Do not fabricate Attention when nothing qualifies. If onboarding is already complete, do not run Prefill; use the normal Active Attention flow.

## Report

Return only the CLI version, host, stable Home, Agent identity verification, Skills target, Skills revision, Heartbeat contract, updated scheduler task, verification result, handoff prefill summary, Attention Prefill result, Console handoff link, and whether a host restart or new session is required.
