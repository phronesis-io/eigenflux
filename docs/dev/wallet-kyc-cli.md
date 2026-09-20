# Wallet KYC CLI

The CLI exposes the Commission backend's KYC API for the **currently bound
Alipay account**. It does not bind a new account or accept a login/account
override. Use the active Agent Home and configured Commission endpoint.
The browser handoff requires CLI version 0.0.107 or later.

## Commands

```text
eigenflux wallet kyc get --format json
eigenflux wallet kyc start --stdin --idempotency-key <attempt-key> --format json
eigenflux wallet kyc authorize <verification-id> --format json
eigenflux wallet kyc complete <verification-id> --stdin --idempotency-key <completion-key> --format json
```

`start` reads exactly one JSON object from stdin:

```json
{"user_name":"<legal name>","cert_no":"<18-character mainland identity-card number>"}
```

`complete` reads exactly one JSON object from stdin:

```json
{"authorization":"<fresh id_verify auth_code>"}
```

Supply private JSON through a trusted process pipe. Never put identity data or
authorization codes in command arguments, shell history, logs, shared files or
Agent conversation transcripts. Input is limited to 4096 bytes; unknown fields
including `agent_id`, `logon_id` and account identifiers are rejected. The CLI
does not persist the input. Successful output is allowlisted to verification
IDs, status, timestamps and browser handoff fields; upstream diagnostic messages/details are not echoed.
HTTP business error codes and retry-after information remain available.

An explicit, non-sensitive 1–64 byte idempotency key is required for start and
manual complete. Reuse the same start key and exact input for transport retries. Use a
new key when intentionally starting another attempt after rejection or expiry.

## Flow

1. Bind an Alipay payout account using the existing account-authorization flow.
2. Run `wallet kyc start` with the owner's consent. Output has
   `verification.verification_id`, `binding_id`, `state`, millisecond timestamps,
   `authorization_url`, and `authorization_expires_at` while pending. IDs remain decimal strings.
3. Open the returned one-use `authorization_url` in the owner's browser. Do not
   prefetch or share the link. The API redirects to Alipay `scope=id_verify` and
   handles the registered callback in the same browser using a secure cookie.
4. Authorize the currently bound Alipay account. The callback validates one-use
   state and provider verification ID, then invokes CompleteKYC automatically.
   The owner does not need to copy an auth_code into the CLI. User consent is
   not automated, and the generic result page does not assert a passed KYC.
5. Run `wallet kyc get` to inspect the current binding's latest status. Status
   and complete output omit the provider `verify_id` and authorization link.

For an existing pending attempt, `wallet kyc authorize <verification-id>`
returns a fresh link without identity resubmission or a new attempt. Links last
at most ten minutes, bounded by KYC expiry. After a lost/failed callback, read
status first; obtain another link only if the attempt remains pending. Manual
`complete` is still available for external integrations with a validated fresh
id_verify auth_code; ordinary binding codes cannot be reused.

States are `not_assessed`, `preparing`, `pending`, `verified`, `rejected`,
`failed` or `expired`. Pending attempts expire after two hours. A provider error
is not proof of a mismatch; get status before retrying an uncertain completion.
Expired/failed attempts require a new start; a consumed code without a cached
result requires fresh consent. Rebinding requires new KYC. Starting another
attempt makes it current, even if an older attempt verified.

`verified` only satisfies the KYC gate. Platform wallet/account bans, balances,
waiting periods (unless explicitly exempted), open-withdrawal checks and actual
payout-provider readiness still apply. Historical provider risk `not_assessed`
does not itself block Alipay payout eligibility. These commands never initiate
a withdrawal, clear a ban or modify provider risk data.

The Commission deployment must include migration 13, updated Wallet/API binaries
and Redis. Configure the API's `ALIPAY_APP_ID`, `ALIPAY_PRODUCTION`,
`ALIPAY_PAYOUT_AUTH_ENABLED=true`, and `ALIPAY_KYC_CALLBACK_URL` with the exact
registered HTTPS URL ending in `/api/v1/public/wallet/kyc/callback`. The API
needs no Alipay signing keys. CLI start requests browser support before provider
initiation; missing configuration fails instead of silently returning no link.
Upgrade the reverse proxy too: route `/api/v1/public/wallet/kyc/*` to Commission
and exclude these URLs from access logs/tracing because they carry one-use
tickets or codes. The supplied Caddy configurations do both. No live provider
authorization has been tested by the fixture suite.
The shared capability registry includes these CLI commands. No wallet
gateway/BFF handlers or Dashboard UI are changed by this addition.

## Validation

From `cli/`:

```sh
go test ./...
go test -race ./cmd -run TestWalletKYC -count=1
go vet ./...
go build -o ../build/cli/eigenflux-kyc .
```

Tests use local HTTP fixtures and temporary Agent Homes, not live identity data
or real Alipay authorization. The build above is a local test binary, not a
signed release bundle.
