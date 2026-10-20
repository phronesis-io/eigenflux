package testutil

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/agentidentity"
)

func loginSuccessData(t *testing.T, data map[string]interface{}) map[string]interface{} {
	t.Helper()
	if token, ok := data["access_token"].(string); ok && token != "" {
		return data
	}

	challengeID, ok := data["challenge_id"].(string)
	if !ok || challengeID == "" {
		t.Fatalf("expected either access_token or challenge_id in login response, got %v", data)
	}

	verifyResp := LoginVerifyOTP(t, challengeID, GetMockOTP(t))
	if int(verifyResp["code"].(float64)) != 0 {
		t.Fatalf("login verify failed: %v", verifyResp["msg"])
	}
	return verifyResp["data"].(map[string]interface{})
}

// LoginStart calls POST /api/v1/auth/login and returns the response data map.
func LoginStart(t *testing.T, email string) map[string]interface{} {
	t.Helper()
	body := map[string]string{
		"login_method": "email",
		"email":        email,
	}
	resp := DoPost(t, "/api/v1/auth/login", body, "")
	if int(resp["code"].(float64)) != 0 {
		t.Fatalf("login start failed: %v", resp["msg"])
	}
	return resp["data"].(map[string]interface{})
}

// LoginStartRaw calls POST /api/v1/auth/login and returns the full response (including code/msg).
func LoginStartRaw(t *testing.T, body map[string]string) map[string]interface{} {
	t.Helper()
	return DoPost(t, "/api/v1/auth/login", body, "")
}

// LoginVerifyOTP calls POST /api/v1/auth/login/verify with an OTP code.
func LoginVerifyOTP(t *testing.T, challengeID, code string) map[string]interface{} {
	t.Helper()
	body := map[string]string{
		"login_method": "email",
		"challenge_id": challengeID,
		"code":         code,
	}
	return DoPost(t, "/api/v1/auth/login/verify", body, "")
}

// GetMockOTP returns the mock universal OTP for whitelist-matched requests.
func GetMockOTP(t *testing.T) string {
	t.Helper()
	otp := os.Getenv("MOCK_UNIVERSAL_OTP")
	if otp == "" {
		otp = "123456"
	}
	return otp
}

// LoginAndGetToken seeds a legacy fixture if absent, then performs real login.
func LoginAndGetToken(t *testing.T, email string) (token string, agentID int64, isNew bool) {
	t.Helper()
	EnsureLegacyTestAgent(t, email)
	startData := LoginStart(t, email)
	data := loginSuccessData(t, startData)
	if _, ok := data["agent_id"].(string); !ok {
		t.Fatalf("expected login data.agent_id as string, got %T", data["agent_id"])
	}
	return data["access_token"].(string),
		MustID(t, data["agent_id"], "agent_id"),
		data["is_new_agent"].(bool)
}

// CleanupTestEmails cleans up all DB records and Redis rate-limit keys
// for the given emails.
func CleanupTestEmails(t *testing.T, emails ...string) {
	t.Helper()
	ctx := context.Background()
	rdb := GetTestRedis()

	for _, email := range emails {
		var agentID int64
		err := TestDB.QueryRow("SELECT agent_id FROM agents WHERE email = $1", email).Scan(&agentID)
		if err == nil {
			TestDB.Exec("DELETE FROM agent_sessions WHERE agent_id = $1", agentID)
			TestDB.Exec("DELETE FROM processed_items WHERE item_id IN (SELECT item_id FROM raw_items WHERE author_agent_id = $1)", agentID)
			TestDB.Exec("DELETE FROM raw_items WHERE author_agent_id = $1", agentID)
			TestDB.Exec("DELETE FROM agent_profiles WHERE agent_id = $1", agentID)
			TestDB.Exec("DELETE FROM agents WHERE agent_id = $1", agentID)
		}
		TestDB.Exec("DELETE FROM auth_email_challenges WHERE email = $1", email)

		h := sha256.Sum256([]byte(email))
		emailHash := hex.EncodeToString(h[:])
		rdb.Del(ctx, "auth:login:email:active:"+emailHash)
	}

	for _, ip := range []string{"127.0.0.1", "::1", "[::1]"} {
		rdb.Del(ctx, "auth:login:start:email:ip:"+ip)
		rdb.Del(ctx, "auth:login:verify:email:ip:"+ip)
	}
}

// Sha256Hex is a utility for verifying token hashing.
func Sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// EnsureLegacyTestAgent creates test data without using the retired registration API.
// Existing identities and their profiles remain unchanged.
func EnsureLegacyTestAgent(t *testing.T, email string) {
	t.Helper()
	email = strings.ToLower(strings.TrimSpace(email))
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatal(err)
	}
	id := int64(binary.BigEndian.Uint64(bytes[:]) & ((1 << 63) - 1))
	shortID, err := agentidentity.GenerateShortID()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := TestDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	if _, err := tx.Exec(`INSERT INTO agents (agent_id,short_id,email,email_kind,agent_name,bio,created_at,updated_at)
 VALUES ($1,$2,$3,'legacy_real','','',$4,$4) ON CONFLICT (email) DO NOTHING`, id, shortID, email, now); err != nil {
		t.Fatalf("seed legacy Agent: %v", err)
	}
	if err := tx.QueryRow("SELECT agent_id FROM agents WHERE email=$1", email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO agent_profiles (agent_id,status,updated_at) VALUES ($1,0,$2) ON CONFLICT (agent_id) DO NOTHING`, id, now); err != nil {
		t.Fatalf("seed legacy profile: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
