package auth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/tests/testutil"
)

func TestMain(m *testing.M) {
	testutil.RunTestMain(m)
}

func mockWhitelistedIP(t *testing.T) string {
	t.Helper()
	for _, raw := range strings.Split(os.Getenv("MOCK_OTP_IP_WHITELIST"), ",") {
		ip := strings.TrimSpace(raw)
		if ip != "" {
			return ip
		}
	}
	t.Skip("MOCK_OTP_IP_WHITELIST is not configured")
	return ""
}

func doPostWithIPHeader(t *testing.T, path string, body interface{}, ip string) map[string]interface{} {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request failed: %v", err)
	}
	req, err := http.NewRequest("POST", testutil.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Real-IP", ip)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s failed: %v", path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response failed: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("failed to parse response from POST %s: %v, body: %s", path, err, string(respBody))
	}
	return result
}

func TestAuthLoginFlow(t *testing.T) {
	testutil.WaitForAPI(t)
	emailVerificationEnabled := config.Load().EnableEmailVerification
	allEmails := []string{
		"auth_new@test.com", "auth_existing@test.com",
		"auth_wrongotp@test.com", "auth_maxattempts@test.com",
		"auth_replay@test.com", "auth_expired@test.com",
		"auth_case_identity@test.com", "auth_case_cooldown@test.com",
		"auth_cooldown_ip_limit@test.com",
		"auth_mock_start_bypass@test.com", "auth_mock_verify_bypass@test.com",
	}
	testutil.CleanupTestEmails(t, allEmails...)

	t.Run("NewUser_Login", func(t *testing.T) {
		email := "auth_new@test.com"
		t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

		startData := testutil.LoginStart(t, email)
		if emailVerificationEnabled {
			challengeID := startData["challenge_id"].(string)
			expiresIn := int(startData["expires_in_sec"].(float64))
			resendAfter := int(startData["resend_after_sec"].(float64))

			if challengeID == "" {
				t.Fatal("expected non-empty challenge_id")
			}
			if expiresIn <= 0 {
				t.Fatalf("expected positive expires_in_sec, got %d", expiresIn)
			}
			if resendAfter <= 0 {
				t.Fatalf("expected positive resend_after_sec, got %d", resendAfter)
			}
			if startData["verification_required"] != true {
				t.Fatalf("expected verification_required=true, got %v", startData["verification_required"])
			}
			t.Logf("Challenge created: id=%s, expires_in=%d, resend_after=%d", challengeID, expiresIn, resendAfter)

			otp := testutil.GetMockOTP(t)
			if len(otp) != 6 {
				t.Fatalf("expected 6-digit OTP, got %q", otp)
			}
		} else {
			if startData["verification_required"] != false {
				t.Fatalf("expected verification_required=false, got %v", startData["verification_required"])
			}
			if _, ok := startData["challenge_id"]; ok {
				t.Fatalf("expected direct login without challenge_id, got %v", startData["challenge_id"])
			}
		}

		data := startData
		if _, ok := startData["access_token"].(string); !ok || startData["access_token"].(string) == "" {
			challengeID := startData["challenge_id"].(string)
			verifyResp := testutil.LoginVerifyOTP(t, challengeID, testutil.GetMockOTP(t))
			if int(verifyResp["code"].(float64)) != 0 {
				t.Fatalf("verify failed: %v", verifyResp["msg"])
			}
			data = verifyResp["data"].(map[string]interface{})
		}
		if !data["is_new_agent"].(bool) {
			t.Error("expected is_new_agent=true for new email")
		}
		if !data["needs_profile_completion"].(bool) {
			t.Error("expected needs_profile_completion=true for new agent")
		}
		accessToken := data["access_token"].(string)
		if accessToken == "" {
			t.Fatal("expected non-empty access_token")
		}
		expiresAt := int64(data["expires_at"].(float64))
		if expiresAt <= time.Now().UnixMilli() {
			t.Fatal("expected expires_at in the future")
		}
		t.Logf("New agent registered: id=%v, is_new=%v, token=%s...",
			data["agent_id"], data["is_new_agent"], accessToken[:8])

		agentData := testutil.GetAgent(t, accessToken)
		profile := agentData["profile"].(map[string]interface{})
		if profile["email"].(string) != email {
			t.Fatalf("expected email=%s, got %s", email, profile["email"])
		}
	})

	t.Run("ExistingUser_LoginOnly", func(t *testing.T) {
		email := "auth_existing@test.com"
		t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

		testutil.LoginAndGetToken(t, email)
		if emailVerificationEnabled {
			h := sha256.Sum256([]byte(email))
			testutil.GetTestRedis().Del(context.Background(), "auth:login:email:cooldown:"+hex.EncodeToString(h[:]))
		}

		token, _, isNew := testutil.LoginAndGetToken(t, email)
		if isNew {
			t.Error("expected is_new_agent=false for existing email")
		}

		agentData := testutil.GetAgent(t, token)
		profile := agentData["profile"].(map[string]interface{})
		if profile["email"].(string) != email {
			t.Fatalf("expected email=%s, got %s", email, profile["email"])
		}
		t.Log("Existing user logged in successfully with new session")
	})

	t.Run("EmailNormalization_SameMailboxSameAgent", func(t *testing.T) {
		emailLower := "auth_case_identity@test.com"
		emailUpper := "AUTH_CASE_IDENTITY@TEST.COM"
		t.Cleanup(func() { testutil.CleanupTestEmails(t, emailLower, emailUpper) })

		startA := testutil.LoginStart(t, emailUpper)
		dataA := startA
		if _, ok := startA["access_token"].(string); !ok || startA["access_token"].(string) == "" {
			challengeA := startA["challenge_id"].(string)
			verifyA := testutil.LoginVerifyOTP(t, challengeA, testutil.GetMockOTP(t))
			if int(verifyA["code"].(float64)) != 0 {
				t.Fatalf("first verify failed: %v", verifyA["msg"])
			}
			dataA = verifyA["data"].(map[string]interface{})
		}
		agentIDA := testutil.MustID(t, dataA["agent_id"], "agent_id")
		if !dataA["is_new_agent"].(bool) {
			t.Fatal("expected first login to create new agent")
		}

		if emailVerificationEnabled {
			h := sha256.Sum256([]byte(emailLower))
			testutil.GetTestRedis().Del(context.Background(), "auth:login:email:cooldown:"+hex.EncodeToString(h[:]))
		}

		startB := testutil.LoginStart(t, emailLower)
		dataB := startB
		if _, ok := startB["access_token"].(string); !ok || startB["access_token"].(string) == "" {
			challengeB := startB["challenge_id"].(string)
			verifyB := testutil.LoginVerifyOTP(t, challengeB, testutil.GetMockOTP(t))
			if int(verifyB["code"].(float64)) != 0 {
				t.Fatalf("second verify failed: %v", verifyB["msg"])
			}
			dataB = verifyB["data"].(map[string]interface{})
		}
		agentIDB := testutil.MustID(t, dataB["agent_id"], "agent_id")
		if dataB["is_new_agent"].(bool) {
			t.Fatal("expected second login to be existing user")
		}
		if agentIDA != agentIDB {
			t.Fatalf("expected same agent_id for case-insensitive email, got %d vs %d", agentIDA, agentIDB)
		}
	})

	t.Run("EmailNormalization_CooldownCaseInsensitive", func(t *testing.T) {
		emailLower := "auth_case_cooldown@test.com"
		emailUpper := "AUTH_CASE_COOLDOWN@TEST.COM"
		t.Cleanup(func() { testutil.CleanupTestEmails(t, emailLower, emailUpper) })

		resp1 := testutil.LoginStartRaw(t, map[string]string{
			"login_method": "email",
			"email":        emailUpper,
		})
		if int(resp1["code"].(float64)) != 0 {
			t.Fatalf("first login start failed: %v", resp1["msg"])
		}

		resp2 := testutil.LoginStartRaw(t, map[string]string{
			"login_method": "email",
			"email":        emailLower,
		})
		if emailVerificationEnabled {
			if int(resp2["code"].(float64)) != 429 {
				t.Fatalf("expected case-insensitive cooldown hit (429), got code=%v msg=%v", resp2["code"], resp2["msg"])
			}
		} else {
			if int(resp2["code"].(float64)) != 0 {
				t.Fatalf("expected second direct login to succeed, got code=%v msg=%v", resp2["code"], resp2["msg"])
			}
		}
	})

	if emailVerificationEnabled {
		t.Run("CooldownRetries_DoNotConsumeStartIPQuota", func(t *testing.T) {
			email := "auth_cooldown_ip_limit@test.com"
			ip := "203.0.113.20"
			t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

			ctx := context.Background()
			rdb := testutil.GetTestRedis()
			emailHash := sha256.Sum256([]byte(email))
			cooldownKey := "auth:login:email:cooldown:" + hex.EncodeToString(emailHash[:])
			ipKey := "auth:login:start:email:ip:" + ip
			if err := rdb.Del(ctx, cooldownKey, ipKey).Err(); err != nil {
				t.Fatalf("failed to reset cooldown/rate-limit keys: %v", err)
			}

			first := doPostWithIPHeader(t, "/api/v1/auth/login", map[string]string{
				"login_method": "email",
				"email":        email,
			}, ip)
			if int(first["code"].(float64)) != 0 {
				t.Fatalf("first login start failed: %v", first["msg"])
			}

			for i := 0; i < 10; i++ {
				resp := doPostWithIPHeader(t, "/api/v1/auth/login", map[string]string{
					"login_method": "email",
					"email":        email,
				}, ip)
				if int(resp["code"].(float64)) != 429 {
					t.Fatalf("expected cooldown response on retry %d, got code=%v msg=%v", i+1, resp["code"], resp["msg"])
				}
				if msg := fmt.Sprint(resp["msg"]); msg != "too many requests, please wait before retrying" {
					t.Fatalf("expected cooldown response on retry %d, got msg=%v", i+1, resp["msg"])
				}
			}

			counter, err := rdb.Get(ctx, ipKey).Result()
			if err != nil {
				t.Fatalf("failed to read start rate limit key: %v", err)
			}
			if counter != "1" {
				t.Fatalf("expected cooldown retries to leave start rate limit key at 1, got %s", counter)
			}
		})

		t.Run("MockWhitelist_BypassesStartIPRateLimit", func(t *testing.T) {
			email := "auth_mock_start_bypass@test.com"
			ip := mockWhitelistedIP(t)
			t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

			ctx := context.Background()
			rdb := testutil.GetTestRedis()
			key := "auth:login:start:email:ip:" + ip
			if err := rdb.Set(ctx, key, "10", 10*time.Minute).Err(); err != nil {
				t.Fatalf("failed to seed start rate limit key: %v", err)
			}

			resp := doPostWithIPHeader(t, "/api/v1/auth/login", map[string]string{
				"login_method": "email",
				"email":        email,
			}, ip)
			if int(resp["code"].(float64)) != 0 {
				t.Fatalf("expected mock allowlisted start to bypass IP limit, got code=%v msg=%v", resp["code"], resp["msg"])
			}

			counter, err := rdb.Get(ctx, key).Result()
			if err != nil {
				t.Fatalf("failed to read start rate limit key: %v", err)
			}
			if counter != "10" {
				t.Fatalf("expected start rate limit key to remain 10, got %s", counter)
			}
		})

		t.Run("WrongOTP_IncrementAttempt", func(t *testing.T) {
			email := "auth_wrongotp@test.com"
			t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

			startData := testutil.LoginStart(t, email)
			challengeID := startData["challenge_id"].(string)

			resp := testutil.LoginVerifyOTP(t, challengeID, "000000")
			code := int(resp["code"].(float64))
			if code == 0 {
				t.Fatal("expected failure for wrong OTP")
			}
			t.Logf("Wrong OTP correctly rejected: code=%d, msg=%v", code, resp["msg"])
		})

		t.Run("MaxAttempts_ChallengeExhausted", func(t *testing.T) {
			email := "auth_maxattempts@test.com"
			t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

			startData := testutil.LoginStart(t, email)
			challengeID := startData["challenge_id"].(string)
			otp := testutil.GetMockOTP(t)

			for i := 0; i < 5; i++ {
				resp := testutil.LoginVerifyOTP(t, challengeID, "000000")
				code := int(resp["code"].(float64))
				if code == 0 {
					t.Fatal("expected failure for wrong OTP")
				}
			}

			resp := testutil.LoginVerifyOTP(t, challengeID, otp)
			code := int(resp["code"].(float64))
			if code == 0 {
				t.Fatal("expected failure after max attempts exhausted")
			}
			t.Log("Challenge correctly exhausted after max attempts")
		})

		t.Run("MockWhitelist_BypassesVerifyIPRateLimit", func(t *testing.T) {
			email := "auth_mock_verify_bypass@test.com"
			ip := mockWhitelistedIP(t)
			t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

			startResp := doPostWithIPHeader(t, "/api/v1/auth/login", map[string]string{
				"login_method": "email",
				"email":        email,
			}, ip)
			if int(startResp["code"].(float64)) != 0 {
				t.Fatalf("login start failed: %v", startResp["msg"])
			}

			ctx := context.Background()
			rdb := testutil.GetTestRedis()
			key := "auth:login:verify:email:ip:" + ip
			if err := rdb.Set(ctx, key, "30", 10*time.Minute).Err(); err != nil {
				t.Fatalf("failed to seed verify rate limit key: %v", err)
			}

			verifyResp := doPostWithIPHeader(t, "/api/v1/auth/login/verify", map[string]string{
				"login_method": "email",
				"challenge_id": startResp["data"].(map[string]interface{})["challenge_id"].(string),
				"code":         testutil.GetMockOTP(t),
			}, ip)
			if int(verifyResp["code"].(float64)) != 0 {
				t.Fatalf("expected mock allowlisted verify to bypass IP limit, got code=%v msg=%v", verifyResp["code"], verifyResp["msg"])
			}

			counter, err := rdb.Get(ctx, key).Result()
			if err != nil {
				t.Fatalf("failed to read verify rate limit key: %v", err)
			}
			if counter != "30" {
				t.Fatalf("expected verify rate limit key to remain 30, got %s", counter)
			}
		})

		t.Run("Replay_ConsumedChallenge", func(t *testing.T) {
			email := "auth_replay@test.com"
			t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

			startData := testutil.LoginStart(t, email)
			challengeID := startData["challenge_id"].(string)
			otp := testutil.GetMockOTP(t)

			verifyResp := testutil.LoginVerifyOTP(t, challengeID, otp)
			if int(verifyResp["code"].(float64)) != 0 {
				t.Fatalf("first verify should succeed: %v", verifyResp["msg"])
			}

			replayResp := testutil.LoginVerifyOTP(t, challengeID, otp)
			code := int(replayResp["code"].(float64))
			if code == 0 {
				t.Fatal("expected failure for replay of consumed challenge")
			}
			t.Log("Replay of consumed challenge correctly rejected")
		})

		t.Run("Expired_Challenge", func(t *testing.T) {
			email := "auth_expired@test.com"
			t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

			startData := testutil.LoginStart(t, email)
			challengeID := startData["challenge_id"].(string)
			otp := testutil.GetMockOTP(t)

			_, err := testutil.TestDB.Exec(
				"UPDATE auth_email_challenges SET expire_at = $1 WHERE challenge_id = $2",
				time.Now().Add(-1*time.Minute).UnixMilli(), challengeID,
			)
			if err != nil {
				t.Fatalf("failed to expire challenge: %v", err)
			}

			resp := testutil.LoginVerifyOTP(t, challengeID, otp)
			code := int(resp["code"].(float64))
			if code == 0 {
				t.Fatal("expected failure for expired challenge")
			}
			t.Log("Expired challenge correctly rejected")
		})
	}

	t.Run("InvalidLoginMethod_Start", func(t *testing.T) {
		resp := testutil.LoginStartRaw(t, map[string]string{
			"login_method": "phone",
			"email":        "test@test.com",
		})
		code := int(resp["code"].(float64))
		if code == 0 {
			t.Fatal("expected failure for unsupported login_method")
		}
		t.Logf("Invalid login_method correctly rejected: code=%d", code)
	})

	t.Run("InvalidLoginMethod_Verify", func(t *testing.T) {
		if !emailVerificationEnabled {
			t.Skip("verify endpoint is not used when email verification is disabled")
		}
		resp := testutil.DoPost(t, "/api/v1/auth/login/verify", map[string]string{
			"login_method": "phone",
			"challenge_id": "fake",
			"code":         "123456",
		}, "")
		code := int(resp["code"].(float64))
		if code == 0 {
			t.Fatal("expected failure for unsupported login_method in verify")
		}
		t.Logf("Invalid login_method in verify correctly rejected: code=%d", code)
	})

	t.Run("NonexistentChallenge", func(t *testing.T) {
		if !emailVerificationEnabled {
			t.Skip("verify endpoint is not used when email verification is disabled")
		}
		resp := testutil.LoginVerifyOTP(t, "ch_nonexistent_999", "123456")
		code := int(resp["code"].(float64))
		if code == 0 {
			t.Fatal("expected failure for nonexistent challenge_id")
		}
		t.Logf("Nonexistent challenge correctly rejected: code=%d", code)
	})
}

func TestAuthSessionValidation(t *testing.T) {
	testutil.WaitForAPI(t)
	email := "auth_session@test.com"
	testutil.CleanupTestEmails(t, email)
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	t.Run("NoAuthHeader_Returns401", func(t *testing.T) {
		req, _ := http.NewRequest("GET", testutil.BaseURL+"/api/v1/agents/me", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
		t.Log("No auth header correctly returns 401")
	})

	t.Run("InvalidToken_Returns401", func(t *testing.T) {
		req, _ := http.NewRequest("GET", testutil.BaseURL+"/api/v1/agents/me", nil)
		req.Header.Set("Authorization", "Bearer invalid_token_abc123")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
		t.Log("Invalid token correctly returns 401")
	})

	t.Run("ValidToken_AccessProtectedEndpoints", func(t *testing.T) {
		token, _, _ := testutil.LoginAndGetToken(t, email)
		agentData := testutil.GetAgent(t, token)
		profile := agentData["profile"].(map[string]interface{})
		if profile["email"].(string) != email {
			t.Fatalf("expected email=%s, got %s", email, profile["email"])
		}
		t.Log("Valid session token accesses protected endpoint successfully")
	})

	t.Run("TokenHash_NotPlaintext", func(t *testing.T) {
		email := "auth_session@test.com"
		var agentID int64
		err := testutil.TestDB.QueryRow("SELECT agent_id FROM agents WHERE email = $1", email).Scan(&agentID)
		if err != nil {
			t.Fatalf("failed to find agent: %v", err)
		}

		var tokenHash string
		err = testutil.TestDB.QueryRow(
			"SELECT token_hash FROM agent_sessions WHERE agent_id = $1 AND status = 0 ORDER BY created_at DESC LIMIT 1",
			agentID,
		).Scan(&tokenHash)
		if err != nil {
			t.Fatalf("failed to get session: %v", err)
		}

		if len(tokenHash) != 64 {
			t.Fatalf("expected 64-char hex hash, got %d chars: %s", len(tokenHash), tokenHash)
		}
		_, err = hex.DecodeString(tokenHash)
		if err != nil {
			t.Fatalf("token_hash is not valid hex: %v", err)
		}
		t.Log("Token stored as hash, not plaintext")
	})
}

func TestAuthProfileCompletion(t *testing.T) {
	testutil.WaitForAPI(t)
	email := "auth_profile@test.com"
	testutil.CleanupTestEmails(t, email)
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	token, _, isNew := testutil.LoginAndGetToken(t, email)
	if !isNew {
		t.Fatal("expected is_new_agent=true")
	}

	agentData := testutil.GetAgent(t, token)
	profile := agentData["profile"].(map[string]interface{})
	if profile["agent_name"].(string) != "" {
		t.Errorf("expected empty agent_name for new agent, got %q", profile["agent_name"])
	}
	if profile["bio"].(string) != "" {
		t.Errorf("expected empty bio for new agent, got %q", profile["bio"])
	}

	updateBody := map[string]string{
		"agent_name": "TestBot",
		"bio":        "I am interested in AI and machine learning",
	}
	updateResp := testutil.DoPut(t, "/api/v1/agents/profile", updateBody, token)
	if int(updateResp["code"].(float64)) != 0 {
		t.Fatalf("profile update failed: %v", updateResp["msg"])
	}
	if updateResp["msg"].(string) != "Registration completed. You can now start browsing your feed." {
		t.Fatalf("expected registration completion message, got %q", updateResp["msg"])
	}

	agentData2 := testutil.GetAgent(t, token)
	profile2 := agentData2["profile"].(map[string]interface{})
	if profile2["agent_name"].(string) != "TestBot" {
		t.Errorf("expected agent_name=TestBot, got %q", profile2["agent_name"])
	}
	if profile2["bio"].(string) != "I am interested in AI and machine learning" {
		t.Errorf("expected bio updated, got %q", profile2["bio"])
	}
	pca2, ok := verifyProfileCompletedAtAfterRelogin(t, email)
	if !ok {
		t.Error("expected profile_completed_at to be set after completing profile")
	} else {
		t.Logf("profile_completed_at set to %v", pca2)
	}
	t.Log("Profile completion flow working correctly")
}

func TestAuthProfileCompletionRequiresAgentName(t *testing.T) {
	testutil.WaitForAPI(t)
	email := "auth_profile_custom@test.com"
	testutil.CleanupTestEmails(t, email)
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	token, _, isNew := testutil.LoginAndGetToken(t, email)
	if !isNew {
		t.Fatal("expected is_new_agent=true")
	}

	updateBody := map[string]string{
		"bio": "I track AI agent systems",
	}
	updateResp := testutil.DoPut(t, "/api/v1/agents/profile", updateBody, token)
	if int(updateResp["code"].(float64)) != 0 {
		t.Fatalf("profile update failed: %v", updateResp["msg"])
	}
	if updateResp["msg"].(string) != "success" {
		t.Fatalf("expected success message for partial profile update, got %q", updateResp["msg"])
	}

	agentData := testutil.GetAgent(t, token)
	profile := agentData["profile"].(map[string]interface{})
	if profile["agent_name"].(string) != "" {
		t.Fatalf("expected agent_name to remain empty, got %q", profile["agent_name"])
	}

	verifyData := reloginAndVerify(t, email)
	if !verifyData["needs_profile_completion"].(bool) {
		t.Fatal("expected needs_profile_completion=true when agent_name remains empty")
	}
}

func verifyProfileCompletedAtAfterRelogin(t *testing.T, email string) (interface{}, bool) {
	t.Helper()
	verifyData := reloginAndVerify(t, email)
	if verifyData["needs_profile_completion"].(bool) {
		t.Error("expected needs_profile_completion=false after profile completion")
	}
	value, ok := verifyData["profile_completed_at"]
	return value, ok && value != nil
}

func reloginAndVerify(t *testing.T, email string) map[string]interface{} {
	t.Helper()
	if config.Load().EnableEmailVerification {
		h := sha256.Sum256([]byte(email))
		testutil.GetTestRedis().Del(context.Background(), "auth:login:email:cooldown:"+hex.EncodeToString(h[:]))
	}
	startData := testutil.LoginStart(t, email)
	if token, ok := startData["access_token"].(string); ok && token != "" {
		return startData
	}

	challengeID := startData["challenge_id"].(string)
	verifyResp := testutil.LoginVerifyOTP(t, challengeID, testutil.GetMockOTP(t))
	if int(verifyResp["code"].(float64)) != 0 {
		t.Fatalf("verify failed: %v", verifyResp["msg"])
	}
	return verifyResp["data"].(map[string]interface{})
}

func TestAuthAntiEnumeration(t *testing.T) {
	testutil.WaitForAPI(t)
	email := "existing_enum_test@test.com"
	testutil.CleanupTestEmails(t, email)
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	resp1 := testutil.LoginStartRaw(t, map[string]string{
		"login_method": "email",
		"email":        email,
	})
	resp2 := testutil.LoginStartRaw(t, map[string]string{
		"login_method": "email",
		"email":        fmt.Sprintf("nonexistent_%d@test.com", time.Now().UnixNano()),
	})

	code1 := int(resp1["code"].(float64))
	code2 := int(resp2["code"].(float64))
	if code1 != code2 {
		t.Errorf("response codes differ: existing=%d, nonexistent=%d (anti-enumeration violation)", code1, code2)
	}

	msg1 := resp1["msg"].(string)
	msg2 := resp2["msg"].(string)
	if msg1 != msg2 {
		t.Errorf("response messages differ: existing=%q, nonexistent=%q (anti-enumeration violation)", msg1, msg2)
	}
	t.Log("Anti-enumeration check passed: responses are identical")
}

func TestAuthVerifyTokenRejected(t *testing.T) {
	testutil.WaitForAPI(t)
	if !config.Load().EnableEmailVerification {
		t.Skip("verify_token is only relevant when email verification is enabled")
	}
	email := "auth_verify_token_rejected@test.com"
	testutil.CleanupTestEmails(t, email)
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })
	startData := testutil.LoginStart(t, email)
	challengeID := startData["challenge_id"].(string)

	resp := testutil.DoPost(t, "/api/v1/auth/login/verify", map[string]string{
		"login_method": "email",
		"challenge_id": challengeID,
		"verify_token": "vt_fake_not_supported",
	}, "")
	code := int(resp["code"].(float64))
	if code != 400 {
		t.Fatalf("expected 400 when verify_token is provided without code, got code=%d msg=%v", code, resp["msg"])
	}
	t.Log("verify_token correctly rejected in OTP-only mode")
}

func TestAuthLogout(t *testing.T) {
	testutil.WaitForAPI(t)
	email := "auth_logout@test.com"
	testutil.CleanupTestEmails(t, email)
	t.Cleanup(func() { testutil.CleanupTestEmails(t, email) })

	token, _, _ := testutil.LoginAndGetToken(t, email)

	// Verify token works before logout.
	agentData := testutil.GetAgent(t, token)
	profile := agentData["profile"].(map[string]interface{})
	if profile["email"].(string) != email {
		t.Fatalf("expected email=%s, got %s", email, profile["email"])
	}

	// Logout.
	logoutResp := testutil.DoPost(t, "/api/v1/auth/logout", nil, token)
	if int(logoutResp["code"].(float64)) != 0 {
		t.Fatalf("logout failed: %v", logoutResp["msg"])
	}

	// Verify the token is now revoked: any authenticated endpoint should return 401.
	req, _ := http.NewRequest("GET", testutil.BaseURL+"/api/v1/agents/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 after logout, got %d", resp.StatusCode)
	}
	t.Log("Token correctly revoked after logout")
}

func TestAuthSQLInjection(t *testing.T) {
	testutil.WaitForAPI(t)

	maliciousEmails := []string{
		"' OR 1=1 --@test.com",
		"test@test.com'; DROP TABLE agents;--",
		"<script>alert('xss')</script>@test.com",
	}

	for _, email := range maliciousEmails {
		resp := testutil.LoginStartRaw(t, map[string]string{
			"login_method": "email",
			"email":        email,
		})
		code := int(resp["code"].(float64))
		if code == 500 {
			t.Errorf("SQL injection attempt caused server error with email: %s", email)
		}
		t.Logf("Handled potentially malicious email %q safely (code=%d)", email, code)
	}
}
