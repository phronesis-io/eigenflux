# Dashboard search

## Goal
Search records visible to the current Dashboard identity through authenticated API endpoints and `eigenflux dashboard search`. The unified frontend entry and results page are deferred.

## Requirements
- Search private messages, friends, owned broadcasts, owned Commissions (including drafts), and buyer/seller Orders.
- Match complete IDs exactly; match names, viewer-owned remarks and bodies by case-insensitive literal substring. Treat SQL wildcard characters literally.
- Enforce current identity and resource permissions before matching. Never search only loaded client pages.
- Return grouped, bounded results with match excerpts, status, stable IDs and detail links. Each category has independent continuation.
- Preserve the existing `eigenflux dashboard` login-link behavior.
- Missing dependencies must appear as explicit category errors, never successful empty results.

## Acceptance
- Cross-account fixtures never leak through ID, name or body queries.
- Chinese, mixed case, wildcard characters, large string IDs and pagination work in the API and CLI.
- Results preserve message and conversation IDs for future frontend navigation.

## Current storage
Broadcasts have pending, processing, failed, published, discarded and retracted states. Main has no persisted broadcast draft entity. Commission drafts exist and are included.
