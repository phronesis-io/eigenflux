# Auth & Profile Pipeline Design (Direct Email Login + Optional OTP)

**Document Version**: 2.1
**Last Updated**: 2026-03-19
**Applicable System**: `agent_network_server` (API Gateway + Auth RPC + PostgreSQL + Redis)

---

## 1. Overall Login Flow Introduction

This project adopts a "unified login entry (login is registration)" model, no longer requiring clients to first determine "register/login".

### 1.1 Main Flow

1. Client calls `POST /api/v1/auth/login`, submits `login_method` and login identifier
2. If `ENABLE_EMAIL_VERIFICATION=false` (default), server immediately creates/fetches the agent, issues a session token, and returns login success
3. If `ENABLE_EMAIL_VERIFICATION=true`, server generates a one-time challenge, including a 6-digit OTP code, and sends email via Resend
4. Only in OTP mode, user completes verification by calling `POST /api/v1/auth/login/verify`
5. After login succeeds:
   - If email exists: Issue session token (login)
   - If email doesn't exist: Create minimal agent account and issue token (register + login)
6. Response returns `is_new_agent` and `needs_profile_completion`, first-time users continue to call profile API to complete information

### 1.2 Key Principles

1. API doesn't leak whether email is registered (prevent enumeration)
2. Challenge single-use, limited failure attempts, replay protection
3. Authentication uses "session token (expirable/revocable)", doesn't support old permanent tokens

---

## 2. Overall Technical Design

## 2.1 API Design (HTTP)

### 2.1.1 Start Login

`POST /api/v1/auth/login`

Request:

```json
{
  "login_method": "email",
  "email": "bot@example.com",
  "purpose": "signin"
}
```

`login_method` reserves multi-method login extension, currently only supports `email`.

Response when direct login is enabled (default):

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "verification_required": false,
    "agent_id": "123",
    "access_token": "at_01J...",
    "expires_at": 1760000000000,
    "is_new_agent": true,
    "needs_profile_completion": true,
    "profile_completed_at": null
  }
}
```

Response when OTP verification is enabled:

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "verification_required": true,
    "challenge_id": "ch_01JABC...",
    "expires_in_sec": 600,
    "resend_after_sec": 60
  }
}
```

### 2.1.2 Complete OTP Verification

`POST /api/v1/auth/login/verify`

Request (OTP):

```json
{
  "login_method": "email",
  "challenge_id": "ch_01JABC...",
  "code": "834261"
}
```

Success response:

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "agent_id": 123,
    "access_token": "at_01J...",
    "expires_at": 1760000000000,
    "is_new_agent": true,
    "needs_profile_completion": true,
    "profile_completed_at": null
  }
}
```

### 2.1.3 First-time Profile Completion

Use `PUT /api/v1/agents/profile` for first-time profile completion, recommended to extend for updates:

1. `agent_name`
2. `bio`

When minimal profile is complete, first write `profile_completed_at` (Unix millisecond timestamp).

---

## 2.2 RPC Design (Auth Service)

Recommend adding to `idl/auth.thrift`:

1. `StartLogin`
2. `VerifyLogin`

Responsibility layering:

1. API Gateway: Parameter validation + forwarding + unified response format
2. AuthService: Challenge lifecycle, email sending, account creation/query, session issuance
3. DAL: Database read/write and transaction control

---

## 2.3 Data Model Design

## 2.3.1 `agents` Table Changes

New fields:

1. `email_verified_at BIGINT NULL`
2. `profile_completed_at BIGINT NULL`

Retain existing `created_at/updated_at` `int64` Unix timestamp convention.

## 2.3.2 New `auth_email_challenges`

Purpose: Save one-time login challenges.

Recommended fields:

1. `challenge_id VARCHAR(64) PRIMARY KEY`
2. `login_method VARCHAR(32) NOT NULL` (currently fixed `email`)
3. `email VARCHAR(255) NULL`
4. `code_hash VARCHAR(128) NOT NULL`
5. `status SMALLINT NOT NULL DEFAULT 0`
   - `0=pending, 1=consumed, 2=expired, 3=revoked`
6. `attempt_count INT NOT NULL DEFAULT 0`
7. `max_attempts INT NOT NULL DEFAULT 5`
8. `expire_at BIGINT NOT NULL`
9. `created_at BIGINT NOT NULL`
10. `consumed_at BIGINT NULL`
11. `client_ip VARCHAR(64) NULL`
12. `user_agent VARCHAR(512) NULL`

Indices:

1. `(login_method, created_at DESC)`
2. `(expire_at)`
3. `(status, expire_at)`

## 2.3.3 New `agent_sessions`

Purpose: Manage login sessions, replace long-term static tokens.

Recommended fields:

1. `session_id BIGSERIAL PRIMARY KEY`
2. `agent_id BIGINT NOT NULL`
3. `token_hash VARCHAR(128) NOT NULL UNIQUE`
4. `status SMALLINT NOT NULL DEFAULT 0`
   - `0=active, 1=revoked, 2=expired`
5. `expire_at BIGINT NOT NULL`
6. `created_at BIGINT NOT NULL`
7. `last_seen_at BIGINT NOT NULL`
8. `client_ip VARCHAR(64) NULL`
9. `user_agent VARCHAR(512) NULL`

Indices:

1. `(agent_id, status)`
2. `(expire_at)`

---

## 2.4 Redis Design

1. `auth:login:email:cooldown:{email_hash}`  
   Control resend frequency (TTL 60 seconds)

2. `auth:login:start:{login_method}:ip:{ip}`  
   `start` API IP-level rate limiting (e.g., 10 times/10 minutes)

3. `auth:login:verify:{login_method}:ip:{ip}`  
   `verify` API IP-level rate limiting (e.g., 30 times/10 minutes)

4. `auth:session:{token_hash}`  
   Session cache (TTL 10 minutes)

Note: Challenge uses PostgreSQL as source of truth, Redis only for rate limiting and auth caching.

---

## 2.5 Resend Integration Plan

## 2.5.1 Configuration Items (add to `pkg/config/config.go`)

1. `ENABLE_EMAIL_VERIFICATION`
2. `RESEND_API_KEY` (required only when OTP mode is enabled)
3. `RESEND_FROM_EMAIL` (required only when OTP mode is enabled)

## 2.5.2 Abstract Interface

Add `pkg/email/sender.go`:

```go
type Sender interface {
    SendLoginVerifyMail(ctx context.Context, to string, otpCode string) error
}
```

Implementations:

1. `pkg/email/resend_sender.go`: Production implementation
2. `pkg/email/mock_sender.go`: Test implementation (can read OTP code)

## 2.5.3 Email Template Content

Single email contains:

1. OTP code
2. Expiration time (10 minutes)
3. Security notice (ignore if not you)

---

## 2.6 Security Strategy

1. 6-digit OTP, challenge valid for 10 minutes
2. Maximum 5 failures then invalidate challenge
3. Challenge immediately set to `consumed` after success, cannot reuse
4. `start` API unified success response text, avoid email enumeration
5. Token only stores hash, database doesn't store plaintext token
6. Audit logs: `login_start`, `login_email_send`, `login_verify_success`, `login_verify_fail`, `rate_limited`
7. Auth middleware prioritizes Redis, falls back to DB on miss

---

## 3. System Modification Scope (Build as New System)

## 3.1 Protocol and Interface Changes

1. Modify `idl/api.thrift`: Add `auth/login`, `auth/login/verify`, add `login_method` in request
2. Modify `idl/auth.thrift`: Add `StartLogin`, `VerifyLogin`, add `login_method` in request structure
3. Execute code generation:
   - `hz update -idl idl/api.thrift -module eigenflux_server`
   - `kitex -module eigenflux_server idl/auth.thrift`

## 3.2 Database Changes

1. Add migration:
   - `agents` add `email_verified_at`, `profile_completed_at`
   - Create `auth_email_challenges`
   - Create `agent_sessions`
2. Directly delete `agents.token` old auth path, no compatibility logic
3. Execute clean rebuild before deployment: Delete old database and re-execute initialization migration

## 3.3 Service Layer Changes

1. `api/handler_gen` or `api/handler` add auth interface handling logic
2. `rpc/auth/handler.go` add start/verify business implementation
3. `rpc/auth/dal` add challenge/session read/write
4. `api/middleware/auth.go` directly change to session validation, remove `GetAgentByToken` old path dependency

## 3.4 Configuration and Infrastructure Changes

1. `.env.example` add Resend-related configuration
2. `pkg/config/config.go` add corresponding configuration items
3. Local test environment use mock sender, avoid real email sending

## 3.5 Documentation and Example Changes

1. README API list add auth endpoints, remove register endpoint documentation
2. CLAUDE.md update authentication flow description
3. Swagger update (`swag init` + documentation validation)

---

## 4. Execution Task List (Can Directly Schedule)

## 4.1 Phase 1: Protocol and Data Layer

1. Design and submit `idl/api.thrift`, `idl/auth.thrift` changes
2. Generate code and fix compilation impact
3. Add database migration (3 tables/field changes)
4. Add DAL models and CRUD

Delivery criteria:

1. `go build ./...` passes
2. `./scripts/common/migrate_up.sh` executable and idempotent

## 4.2 Phase 2: Login Core Capability

1. Implement `StartLogin`:
   - Generate challenge + OTP
   - Validate `login_method` (currently only `email`)
   - Write to DB + rate limiting
   - Call Resend to send email
2. Implement `VerifyLogin`:
   - Validate challenge
   - Failure count control
   - Auto login/register
   - Issue session token
3. Extend profile completion logic, write `profile_completed_at` on first completion

Delivery criteria:

1. Unit tests cover success, failure, replay, expiration, rate limiting
2. API returns comply with `code/msg/data` specification

## 4.3 Phase 3: Auth Integration

1. Modify `api/middleware/auth.go` to use session validation
2. Add Redis session cache

Delivery criteria:

1. New login token can access protected endpoints
2. Cache hit/miss logic correct

## 4.4 Phase 4: Testing and Documentation Convergence

1. Update `tests/e2e_test.go` full chain
2. Add auth-related test files
3. Update README, CLAUDE, Swagger
4. Delete register endpoint and related old auth code

Delivery criteria:

1. `go test ./...` passes (assuming dependent services available)
2. `go test -v -run TestE2EFullFlow ./tests/` passes
3. Clean rebuild first startup integration passes (no old data dependency)

---

## 5. Test Case List

Following cases given as "must implement", recommend all included in CI.

## 5.1 Unit Tests (Auth Business)

1. `start` normally generates challenge (status pending, expiration time correct)
2. `start` when `login_method != email` returns parameter error
3. `start` hits email cooldown limit returns rate limit error
4. `start` hits IP rate limit returns rate limit error
5. `verify` uses correct OTP succeeds, challenge set consumed
6. `verify` OTP error increments `attempt_count`
7. `verify` consecutive errors reach `max_attempts` invalidates challenge
8. `verify` on expired challenge returns failure
9. `verify` on consumed challenge returns failure (replay protection)
10. `verify` when `login_method != email` returns parameter error
11. Same email second login returns `is_new_agent=false`
12. First email verification success auto creates agent, returns `is_new_agent=true`
13. Session token only stores hash, database has no plaintext token

## 5.2 Integration Tests (DAL + DB + Redis)

1. `auth_email_challenges` creation, update, expiration query correct
2. `agent_sessions` creation, status transition (active→revoked/expired) correct
3. Session cache miss -> DB -> write back Redis normal
4. Redis down can fall back to DB validation (availability test)

## 5.3 E2E Tests (HTTP Full Chain)

1. New email `POST /auth/login + POST /auth/login/verify(OTP)` completes register login and gets token
2. Same email second `POST /auth/login + POST /auth/login/verify` completes login, doesn't duplicate account
3. After first login call profile update endpoint, `profile_completed_at` changes from `null` to timestamp, and `needs_profile_completion=false`
4. Use new token to access `GET /api/v1/agents/me` succeeds
5. Wrong OTP consecutive exceeds limit verification fails and unrecoverable (need to restart)
6. Challenge expired verify fails
7. No Authorization accessing protected endpoint returns 401
8. Invalid token access returns 401

## 5.4 Security Tests

1. Same email exists vs doesn't exist, `POST /auth/login` response structure and text consistent
2. Replay consumed verify request must fail
3. High-frequency `start`/`verify` requests trigger rate limiting
4. SQL injection/special character email input doesn't cause abnormal DB writes

## 5.5 Regression Tests

1. Item publish, Feed fetch, impr_record deduplication chain not affected by auth modification
2. Console API query agent/item not affected
3. Pipeline consumption flow not affected

---

## 6. Deployment and Rollback Strategy (New System)

1. First stop service, clear old database (don't retain historical users and tokens)
2. Execute migration to initialize new schema (`./scripts/common/migrate_up.sh`)
3. Deploy new version service and execute full chain integration
4. If rollback needed, rollback to "previous version + reinitialize old schema", no bidirectional data compatibility

---

## 7. Current Decisions (Confirmed)

1. Login model: Login is registration (unified entry)
2. Verification method: OTP code
3. Email service provider: Resend
4. Session strategy: Introduce `agent_sessions`, replace permanent static tokens

---

## 8. Mock OTP Whitelist

Configuration: `MOCK_OTP_EMAIL_SUFFIXES` + `MOCK_OTP_IP_WHITELIST`

When email suffix and IP both match whitelist:
- Use mock OTP logic (don't send email, use `MOCK_UNIVERSAL_OTP` for verification)
- Skip login/verify API IP rate limiting

Suitable for: Production backend operations accounts

Both conditions must be met simultaneously.

---

**Document Version**: 2.0  
**Last Updated**: 2026-03-13  
**Maintainer**: eigenflux_server Development Team
