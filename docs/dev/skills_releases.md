# Signed Skills releases

Official Skills are published from `main` by `.github/workflows/release-skills.yml`.
Automatic pushes and manual dispatches share the `release-skills` concurrency group.
`bash cli/scripts/release-skills.sh` outside Actions dispatches that workflow on
`main`; it requires GitHub workflow permission and does not load local R2 keys.
The legacy `EIGENFLUX_PUBLISH_SKILLS_WITH_CLI=true` switch dispatches the same
workflow, while CLI binary publication remains independent.

## Installation entry

`skills/install.md` is the sole source for the `/install` landing page's Agent
instructions. The page keeps its `/r/<ref>` command; the gateway returns the
installer origin and referral code with a link to
`https://cdn.eigenflux.ai/skills/latest/install.md`. The entry contains no copy
of the installation or onboarding steps. Referral and invite attribution
continue through the existing routes.

After verifying the signed Skills bundle, the same release publishes the raw
installation document to `skills/latest/install.md` with `Cache-Control:
no-store` and checks both R2 and the exact public URL. Every release publishes
the document, including changes only to `skills/install.md`; it is independent
of the signed bundle revision and remains outside the distributable `ef-*`
Skills. Publication or verification failures fail the workflow.

The gateway entry also uses `Cache-Control: no-store`. Once that entry is
deployed, subsequent installation-document changes merged into `main` become
available after Release Skills succeeds, without a website or gateway deploy.
Existing client upgrade flows remain separate. Publish and verify the CDN
document before deploying the gateway entry for the first time.

## Sequence ownership

The release script reads and verifies both R2 latest manifests and the highest
archived signed manifest using the client's Ed25519 trust root. It allocates one
more than the highest accepted sequence. Read, listing, and signature failures
stop publication. `cli/.cli.config`'s `SKILLS_SEQUENCE` is only for local build
artifacts and does not select a production release number.

Before writing latest, the publisher rechecks R2, verifies the signed archive
through a disposable `skills sync`, and reserves
`skills/releases/<sequence>/manifest.json` with `If-None-Match: *`. The archive
and sidecar are also written conditionally under that prefix. Existing release
objects cannot be overwritten by this publisher. Incomplete releases consume
their sequence; the next workflow invocation allocates a larger number. A
workflow rerun is a new release, rather than a rebuild under the old sequence.
Do not delete archived manifests, which retain the release high-water mark.

After the reservation, the publisher uploads archives before manifests to
`skills/latest/` and `cli/latest/`. It verifies both origin manifests and both
CDN manifests, performs a real atomic installation in a disposable directory,
and checks that a repeated sync is a verified no-op. The CDN tarball check uses
the same revision query as clients. No agent credentials or active Skills
folders are involved in this verification.

## Recovery and acceptance

Different contents were previously published with sequence 8. A newly allocated
sequence greater than 8 permits affected clients to accept the current bundle
without deleting local metadata or weakening rollback protection. The regression
test `cli/tests/skills_release_test.go` covers rejecting conflicting signed
sequence 8, atomically accepting sequence 9, preserving unrelated Skills, and
rejecting a later rollback to 8.

For changed content, a successful sync returns `source=skills/latest`,
`verified_manifest=true`, and `atomic=true`. If content is already current,
`source=local` and `atomic=false` are expected; the higher signed sequence is
still persisted and `verified_manifest` remains true.

Production releases require the existing R2 and signing secrets plus Python 3,
Go, GNU tar, and an AWS CLI supporting S3 conditional PutObject. Publishing
through any other workflow is rejected before credentials are loaded.
