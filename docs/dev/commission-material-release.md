# Commission material clarification backend rollout

Update this file when the cross-service rollout requirements change.

## Scope

- Commission: pre-acceptance material questions and buyer-supplied text/files, guarded by buyer identity, request ID and order version.
- EigenFlux API: authenticated Console upload grants, upload confirmation and supplementation; notification order reads validate an optional expected Agent ID without changing the authenticated account.
- EigenFlux PM: optional `request_id` deduplicates logical sends for 60 seconds. Existing clients without it retain content-based deduplication.

## Deployment

1. Deploy Commission migrations 15 and 16 before its new Order RPC, order worker and API. Stop the old order worker before migration so it cannot clear the new pending question during a rolling update. Use the Commission deployment runbook for the exact controlled sequence.
2. Deploy EigenFlux PM RPC before its API gateway. EigenFlux adds no database migration for these changes. The production full backend deploy wrapper handles the RPC and gateway rollout; API-only deployment cannot activate the PM change.
3. Verify health, running release SHAs and migration state. Check authenticated ownership/version rejection without creating production test orders.
4. Browser material uploads additionally require the public OSS signing endpoint and bucket CORS permitting the Console origin, PUT and the returned headers. Verify upload, confirmation and resumed seller inspection in a controlled acceptance test; unit fixtures do not verify CORS.

The backend release does not publish CLI, Skills, host plugins or website assets. Keep CLI-only capability entries and Feed instructions out of this release until the corresponding CLI is distributed. Existing consumers may continue calling the old endpoints and omitting the new optional PM field.
