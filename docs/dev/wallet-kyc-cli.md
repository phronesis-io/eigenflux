# Wallet KYC CLI

The CLI exposes the Commission backend's KYC API for the **currently bound
Alipay account**. It does not bind a new account or accept a login/account
override. Use the active Agent Home and configured Commission endpoint.
These commands require CLI version 0.0.106 or later.

## Commands

```text
eigenflux wallet kyc get --format json
eigenflux wallet kyc start --stdin --idempotency-key <attempt-key> --format json
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
IDs, status and timestamps; upstream diagnostic messages/details are not echoed.
HTTP business error codes and retry-after information remain available.

An explicit, non-sensitive 1–64 byte idempotency key is required for both
mutations. Reuse the same start key and exact input for transport retries. Use a
new key when intentionally starting another attempt after rejection or expiry.

## Flow

1. Bind an Alipay payout account using the existing account-authorization flow.
2. Run `wallet kyc start` with the owner's consent. Output has
   `verification.verification_id`, `binding_id`, `state`, millisecond timestamps,
   and `verify_id` only while initiation is pending. IDs remain decimal strings.
3. Use the returned `verify_id` in the application owner's registered Alipay
   authorization flow: `scope=id_verify`, `cert_verify_id=<verify_id>`, and a
   session-bound one-use state. This low-level CLI does **not** implement a
   callback server, construct OAuth URLs or automate user consent.
4. Supply the resulting fresh `auth_code` to `wallet kyc complete` as
   `authorization`, using the backend `verification_id`, not provider `verify_id`.
   Ordinary binding authorization codes cannot be reused. The backend checks
   the freshly authorized account against the captured binding.
5. Run `wallet kyc get` to inspect the current binding's latest status. Status
   and complete output omit the provider `verify_id`.

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

The Commission deployment must include the KYC endpoints and migration 13.
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
