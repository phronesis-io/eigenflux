# Windows CLI signing

The Release CLI workflow builds on Linux, signs both Windows architectures on
Windows, and publishes only the artifact returned by the successful signing job.
It uses Azure Artifact Signing Public Trust with GitHub OIDC. There is no unsigned
fallback. Missing configuration, signing errors, an untrusted embedded signature,
a different publisher, or a missing timestamp prevents publication.

## Production configuration

Before enabling this release workflow, provision an eligible, identity-validated
Artifact Signing account and a **Public Trust** certificate profile. Private Trust
and test profiles do not establish trust on users' Windows installations. The
account and identity verification must be completed by the organization; repository
code cannot provision a publicly trusted identity on its behalf.

Create the GitHub environment `windows-signing`, restrict deployment branches to
`main`, and configure these environment variables:

| Variable | Value |
| --- | --- |
| `AZURE_CLIENT_ID` | Application/client ID of the signing principal |
| `AZURE_TENANT_ID` | Azure tenant ID |
| `AZURE_SUBSCRIPTION_ID` | Subscription containing the signing account |
| `WINDOWS_SIGNING_ENDPOINT` | Account's regional HTTPS signing endpoint |
| `WINDOWS_SIGNING_ACCOUNT` | Artifact Signing account name |
| `WINDOWS_SIGNING_PROFILE` | Public Trust certificate profile name |
| `WINDOWS_SIGNING_SUBJECT` | Exact full certificate Subject distinguished name |

Set the principal's GitHub federated credential subject to
`repo:phronesis-io/eigenflux:environment:windows-signing`, issuer to
`https://token.actions.githubusercontent.com`, and audience to
`api://AzureADTokenExchange`. Grant only the Artifact Signing Certificate Profile
Signer role on the intended profile. No signing private key or Azure client secret
is stored in GitHub. The subject check deliberately uses the stable publisher
identity instead of a leaf thumbprint because Artifact Signing rotates short-lived
certificates. Obtain the exact subject from the approved profile's certificate.

Signing uses SHA-256 file digests and RFC 3161 SHA-256 timestamps. The verifier
checks Windows Authenticode trust, embedded signature type, expected publisher,
and timestamp presence for both binaries before writing either checksum. It
replaces any pre-signing Windows sidecars with hashes of the final signed bytes.

## Release and acceptance

Merge and deploy the checksum publication work in PR #391 together with this
workflow before the first production release. Resolve workflow conflicts by
keeping #391's publication tests and the build → sign → publish dependency chain.
Use a new CLI version; do not replace previously published versioned binaries.
Publish the signed CLI and verify public delivery before deploying the stricter
PowerShell installer. This PR does not backfill old CDN artifacts or configure
Azure/GitHub resources. Until configuration is complete, Release CLI fails closed.

Manual execution of `build.sh` still creates unsigned developer binaries. Do not
publish those with the low-level `publish.sh`; production releases must use
Release CLI on main. The independent Skills publisher is unchanged.

On a clean Windows 11 device with Smart App Control **On**, download the new
version from `https://cdn.eigenflux.ai/cli/<version>/eigenflux-windows-<arch>.exe`
and its `.sha256`, compare `Get-FileHash -Algorithm SHA256`, and inspect
`Get-AuthenticodeSignature`. Require `Valid`, the expected publisher and timestamp,
then run the official installer and `eigenflux version`. Record Windows version,
architecture, CLI version/hash, SAC state and any Code Integrity 3077 events.
Test amd64 and arm64 on corresponding devices before claiming both verified.
Ordinary hosted CI does not establish SAC enforcement-mode acceptance, and a
valid signature does not promise that every device policy will allow execution.
Keep issue #385 open until production delivery and SAC acceptance are evidenced.

## Tests

`pwsh -File tests/windows_signing/verify-release.tests.ps1` runs on Windows CI.
It rejects an unsigned file and a tampered real signed executable, accepts a
trusted timestamped runner executable, and checks policy failures and final
SHA-256 generation. Policy doubles are separate from real trust tests. They do
not exercise the Azure service or establish EigenFlux publisher trust.

References: [SAC signing](https://learn.microsoft.com/en-us/windows/apps/develop/smart-app-control/code-signing-for-smart-app-control),
[official signing action](https://github.com/Azure/artifact-signing-action).
