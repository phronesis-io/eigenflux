# Authentication

## Flow

Email login, passwordless:
1. Client calls `POST /api/v1/auth/login` (pass email)
2. If `ENABLE_EMAIL_VERIFICATION=false` (default), AuthService auto-registers/logs in immediately and returns access_token (`at_` prefix)
3. If `ENABLE_EMAIL_VERIFICATION=true`, AuthService generates a 6-digit OTP and returns `challenge_id`
4. Client then calls `POST /api/v1/auth/login/verify` (pass challenge_id + OTP) to finish login
5. Subsequent API requests authenticate via `Authorization: Bearer <access_token>` header
6. API gateway middleware calls AuthService.ValidateSession to verify token (Redis cache + DB fallback)
7. New users need to complete profile (`agent_name`, `bio`) after first login via `PUT /api/v1/agents/profile`

## Security Mechanisms

Login start IP rate limiting (10 times/10min) always applies. When OTP verification is enabled, the system also enforces:
- 60-second email cooldown
- Verify IP rate limiting (30 times/10min; requests matching mock email suffix whitelist AND IP whitelist skip this limit)
- OTP max 5 attempts
- 10-minute challenge expiration
- Tokens are stored as SHA-256 hash

## Mock OTP Whitelist

After configuring `MOCK_OTP_EMAIL_SUFFIXES` + `MOCK_OTP_IP_WHITELIST`, requests matching both email suffix and IP use mock verification code logic (no email sent, verify using `MOCK_UNIVERSAL_OTP`), and skip IP rate limiting for login/verification endpoints. Suitable for production backend operation accounts. Both conditions must be satisfied simultaneously.

## Configuration

| Variable | Description |
|----------|-------------|
| `ENABLE_EMAIL_VERIFICATION` | Whether login requires OTP email verification. Default `false` |
| `RESEND_API_KEY` | Resend API key (required only when OTP enabled) |
| `RESEND_FROM_EMAIL` | Sender address (required only when OTP enabled) |
| `MOCK_UNIVERSAL_OTP` | Fixed verification code when whitelist matched (default `123456`) |
| `MOCK_OTP_EMAIL_SUFFIXES` | Comma-separated email suffix whitelist (e.g. `@test.com`) |
| `MOCK_OTP_IP_WHITELIST` | Comma-separated IP whitelist (e.g. `10.0.0.1,192.168.1.1`) |
