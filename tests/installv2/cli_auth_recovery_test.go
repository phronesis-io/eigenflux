package installv2_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/network/standard"
	"github.com/jackc/pgx/v5"
	redis "github.com/redis/go-redis/v9"

	"eigenflux_server/api/consolev2"
	"eigenflux_server/api/install"
	"eigenflux_server/pkg/config"
)

// Exercise automatic and explicit CLI recovery with public registration on and
// off. Every credential and identity file belongs to a temporary Home.
func TestCLIExpiredV2CredentialsRecoverSameAccount(t *testing.T) {
	binary := os.Getenv("EIGENFLUX_TEST_CLI")
	if binary == "" {
		t.Skip("EIGENFLUX_TEST_CLI must point at the CLI built from this checkout")
	}
	if dsn := os.Getenv("PG_DSN"); dsn != "" {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
			t.Fatal("CLI recovery tests require a loopback PostgreSQL host")
		}
	}
	for _, publicRegistration := range []bool{true, false} {
		t.Run(map[bool]string{true: "public_registration_enabled", false: "public_registration_disabled"}[publicRegistration], func(t *testing.T) {
			testCLIExpiredV2CredentialsRecoverSameAccount(t, binary, publicRegistration)
		})
	}
}

func testCLIExpiredV2CredentialsRecoverSameAccount(t *testing.T, binary string, publicRegistration bool) {
	t.Helper()
	h := newHarness(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + listener.Addr().String()
	bootstrapSecret := "installv2-test-broker"
	if !publicRegistration {
		bootstrapSecret = ""
	}
	svc, err := consolev2.NewService(h.db, h.ids, &config.Config{
		ConsoleV2BootstrapSecret: bootstrapSecret,
		ConsoleV2OTPPepper:       "installv2-test-otp-pepper",
		ConsoleV2PublicURL:       testOrigin,
		EnablePublicRegistration: publicRegistration,
		ConsoleV2Registration:    config.RegLimit{WindowSec: 60, IPLimit: 100, SubnetLimit: 100, KeyLimit: 10, GlobalLimit: 100},
	})
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	svc.SetRedisClient(redisClient)
	live := server.New(server.WithListener(listener), server.WithTransport(standard.NewTransporter))
	var registrationCalls, recoveryCalls, provisionCalls, capabilityCalls, handoffCalls atomic.Int32
	live.Use(func(ctx context.Context, c *app.RequestContext) {
		switch string(c.Path()) {
		case "/api/v2/agent-identities/registration-challenges":
			registrationCalls.Add(1)
		case "/api/v2/agent-identities/recovery-challenges":
			recoveryCalls.Add(1)
		case "/api/v2/agent-identities/provision":
			provisionCalls.Add(1)
		case "/api/v2/agent-capabilities":
			capabilityCalls.Add(1)
		case "/api/v2/console/handoffs":
			handoffCalls.Add(1)
		}
		c.Next(ctx)
	})
	svc.Register(live)
	install.Register(live, testOrigin)
	stopped := make(chan error, 1)
	go func() { stopped <- live.Run() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = live.Shutdown(ctx)
		_ = listener.Close()
		select {
		case <-stopped:
		case <-ctx.Done():
			t.Error("test HTTP server did not stop")
		}
	})
	runCLI := func(t *testing.T, home string, args ...string) ([]byte, []byte, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append([]string{"--homedir", home, "--format", "json"}, args...)...)
		command.Env = append(os.Environ(), "HOME="+home, "EIGENFLUX_HOME="+home,
			"EIGENFLUX_SKILLS_DIR="+filepath.Join(home, "skills"), "EIGENFLUX_CDN_URL=http://127.0.0.1:1",
			"EIGENFLUX_HOST=codex", "EIGENFLUX_MODE=skill")
		var stderr bytes.Buffer
		command.Stderr = &stderr
		stdout, err := command.Output()
		return stdout, stderr.Bytes(), err
	}
	runSuccess := func(t *testing.T, home string, args ...string) map[string]any {
		t.Helper()
		stdout, stderr, err := runCLI(t, home, args...)
		if err != nil {
			t.Fatalf("CLI %v: %v stderr=%s", args, err, stderr)
		}
		if len(bytes.TrimSpace(stdout)) == 0 {
			return nil
		}
		var result map[string]any
		if err := json.Unmarshal(stdout, &result); err != nil {
			t.Fatalf("decode CLI %v output: %v", args, err)
		}
		return result
	}
	count := func(t *testing.T, query string, args ...any) int64 {
		t.Helper()
		var result int64
		if err := h.db.Raw(query, args...).Scan(&result).Error; err != nil {
			t.Fatal(err)
		}
		return result
	}
	type profileSnapshot struct{ AgentName, Bio, DraftData, PublicCard, PrivateCard string }
	profile := func(t *testing.T, agentID string) profileSnapshot {
		t.Helper()
		var result profileSnapshot
		if err := h.db.Raw(`SELECT a.agent_name,COALESCE(a.bio,'') AS bio,
			COALESCE((SELECT d.draft_data::text FROM agent_onboarding_drafts d WHERE d.agent_id=a.agent_id ORDER BY revision DESC LIMIT 1),'') AS draft_data,
			COALESCE(c.public_card::text,'') AS public_card,COALESCE(c.private_card::text,'') AS private_card
			FROM agents a LEFT JOIN agent_cards c ON c.agent_id=a.agent_id WHERE a.agent_id=?`, agentID).Scan(&result).Error; err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "revoked_and_expired"}[revoked], func(t *testing.T) {
			home := filepath.Join(t.TempDir(), ".eigenflux")
			runSuccess(t, home, "server", "update", "--name", "eigenflux", "--endpoint", endpoint)
			identity := runSuccess(t, home, "agent", "init")
			id, _ := h.ids.NextID()
			grant := h.json(t, http.MethodPost, "/api/v2/bootstrap-grants", map[string]any{
				"entitlement_id": fmt.Sprintf("cli-recovery-%d", id), "idempotency_key": fmt.Sprintf("cli-recovery-grant-%d", id), "public_key": identity["public_key"],
			}, http.StatusCreated, ut.Header{Key: "X-Bootstrap-Broker-Secret", Value: "installv2-test-broker"})
			ref := h.mint(t, "bilibili", "")
			created := runSuccess(t, home, "agent", "provision", "--bootstrap-grant", grant["bootstrap_grant"].(string), "--nonce", grant["nonce"].(string), "--agent-name", "Original Recovery Agent", "--draft-json", `{"identity_card":{"agent_name":"Original Recovery Agent","bio":"Preserve this profile"}}`, "--ref", ref, "--no-handoff")
			agentID := created["agent_id"].(string)
			if created["created"] != true {
				t.Fatal("test setup did not create a new Agent")
			}
			credentialPath := filepath.Join(home, "servers", "eigenflux", "agent-v2-credentials.json")
			identityPath := filepath.Join(home, "servers", "eigenflux", "identity.json")
			keyBefore, err := os.ReadFile(identityPath)
			if err != nil {
				t.Fatal(err)
			}
			credentialRaw, err := os.ReadFile(credentialPath)
			if err != nil {
				t.Fatal(err)
			}
			var before map[string]any
			if err := json.Unmarshal(credentialRaw, &before); err != nil {
				t.Fatal(err)
			}
			principalID := before["principal_id"].(string)
			before["expires_at"] = float64(time.Now().Add(-time.Minute).UnixMilli())
			expiredRaw, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(credentialPath, expiredRaw, 0600); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UnixMilli()
			update := h.db.Exec(`UPDATE agent_credential_sessions SET issued_at=?,expires_at=?,absolute_expires_at=?,
			revoked_at=CASE WHEN ? THEN ? ELSE revoked_at END
			WHERE principal_id=? AND revoked_at IS NULL AND replaced_by_session_id IS NULL`, now-int64(31*24*time.Hour/time.Millisecond), now-60000, now-1000, revoked, now, principalID)
			if update.Error != nil || update.RowsAffected != 1 {
				t.Fatalf("expire test session: rows=%d err=%v", update.RowsAffected, update.Error)
			}
			originalAgent := h.agent(t, agentID)
			originalProfile := profile(t, agentID)
			agentsBefore := count(t, `SELECT count(*) FROM agents`)
			principalsBefore := count(t, `SELECT count(*) FROM agent_principals WHERE agent_id=?`, agentID)
			sessionsBefore := count(t, `SELECT count(*) FROM agent_credential_sessions WHERE principal_id=?`, principalID)
			handoffsBefore := count(t, `SELECT count(*) FROM console_v2_handoffs WHERE principal_id=?`, principalID)
			registrationCalls.Store(0)
			recoveryCalls.Store(0)
			provisionCalls.Store(0)
			capabilityCalls.Store(0)
			handoffCalls.Store(0)
			stdout, stderr, err := runCLI(t, home, "capabilities", "--lang", "en")
			if revoked {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 4 {
					t.Fatalf("revoked session must exit4: err=%v stderr=%s", err, stderr)
				}
				lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
				var notice struct {
					SchemaVersion        string `json:"schema_version"`
					NotificationRequired bool   `json:"notification_required"`
					ChecksBlocked        bool   `json:"checks_blocked"`
					Home                 string `json:"home"`
					Server               string `json:"server"`
					Code                 string `json:"code"`
				}
				if err := json.Unmarshal([]byte(lines[len(lines)-1]), &notice); err != nil {
					t.Fatalf("missing authentication notice: %v stderr=%s", err, stderr)
				}
				if notice.SchemaVersion != "eigenflux_auth_required.v1" || !notice.NotificationRequired || !notice.ChecksBlocked || notice.Home != home || notice.Server != "eigenflux" || notice.Code != "REFRESH_INVALID" {
					t.Fatalf("incorrect revoked-session notice: %+v", notice)
				}
				if registrationCalls.Load() != 0 || recoveryCalls.Load() != 0 || provisionCalls.Load() != 0 || capabilityCalls.Load() != 0 {
					t.Fatal("revoked session attempted automatic recovery or protected operation")
				}
				if count(t, `SELECT count(*) FROM agent_credential_sessions WHERE principal_id=?`, principalID) != sessionsBefore {
					t.Fatal("revoked session created another credential family")
				}
				after, readErr := os.ReadFile(credentialPath)
				if readErr != nil || !bytes.Equal(after, expiredRaw) {
					t.Fatal("revoked session replaced local credentials")
				}
			} else {
				if err != nil {
					t.Fatalf("expired session did not recover: %v stderr=%s", err, stderr)
				}
				var registry struct {
					SchemaVersion int               `json:"schema_version"`
					Operations    []json.RawMessage `json:"operations"`
				}
				if err := json.Unmarshal(stdout, &registry); err != nil || registry.SchemaVersion != 1 || len(registry.Operations) == 0 {
					t.Fatal("recovered CLI did not receive protected capability registry")
				}
				if registrationCalls.Load() != 0 || recoveryCalls.Load() != 1 || provisionCalls.Load() != 1 || capabilityCalls.Load() != 1 {
					t.Fatalf("unexpected recovery calls: registration=%d recovery=%d provision=%d capabilities=%d", registrationCalls.Load(), recoveryCalls.Load(), provisionCalls.Load(), capabilityCalls.Load())
				}
				var after map[string]any
				afterRaw, readErr := os.ReadFile(credentialPath)
				if readErr != nil || json.Unmarshal(afterRaw, &after) != nil {
					t.Fatal("cannot read recovered local credentials")
				}
				if after["agent_id"] != agentID || after["principal_id"] != principalID || after["access_token"] == before["access_token"] || after["refresh_token"] == before["refresh_token"] || after["expires_at"].(float64) <= float64(time.Now().UnixMilli()) {
					t.Fatal("recovery failed to preserve identity and replace expired credentials")
				}
				if count(t, `SELECT count(*) FROM agent_credential_sessions WHERE principal_id=?`, principalID) != sessionsBefore+1 {
					t.Fatal("automatic recovery did not create exactly one new same-principal session")
				}
				var lifetime int64
				if err := h.db.Raw(`SELECT absolute_expires_at-issued_at FROM agent_credential_sessions WHERE principal_id=? ORDER BY session_id DESC LIMIT 1`, principalID).Scan(&lifetime).Error; err != nil {
					t.Fatal(err)
				}
				if lifetime != int64(30*24*time.Hour/time.Millisecond) {
					t.Fatalf("recovered session lifetime=%d", lifetime)
				}
				if !reflect.DeepEqual(originalAgent, h.agent(t, agentID)) || !reflect.DeepEqual(originalProfile, profile(t, agentID)) {
					t.Fatal("automatic recovery changed profile, account binding, or acquisition")
				}
				reconnected := runSuccess(t, home, "agent", "provision", "--require-existing-agent", "--no-handoff")
				if reconnected["agent_id"] != agentID || reconnected["created"] != false {
					t.Fatalf("explicit reconnect changed the Agent identity: %#v", reconnected)
				}
				reconnectedRaw, readErr := os.ReadFile(credentialPath)
				var reconnectedCredentials map[string]any
				if readErr != nil || json.Unmarshal(reconnectedRaw, &reconnectedCredentials) != nil ||
					reconnectedCredentials["agent_id"] != agentID || reconnectedCredentials["principal_id"] != principalID ||
					reconnectedCredentials["access_token"] == nil || reconnectedCredentials["access_token"] == "" ||
					reconnectedCredentials["refresh_token"] == nil || reconnectedCredentials["refresh_token"] == "" ||
					reconnectedCredentials["access_token"] == after["access_token"] || reconnectedCredentials["refresh_token"] == after["refresh_token"] {
					t.Fatal("explicit reconnect did not replace credentials on the existing Agent and principal")
				}
				if registrationCalls.Load() != 0 || recoveryCalls.Load() != 2 || provisionCalls.Load() != 2 || capabilityCalls.Load() != 1 {
					t.Fatalf("explicit reconnect did not use only the recovery flow: registration=%d recovery=%d provision=%d capabilities=%d", registrationCalls.Load(), recoveryCalls.Load(), provisionCalls.Load(), capabilityCalls.Load())
				}
				if count(t, `SELECT count(*) FROM agent_credential_sessions WHERE principal_id=?`, principalID) != sessionsBefore+2 {
					t.Fatal("explicit reconnect did not create exactly one additional same-principal session")
				}
			}
			keyAfter, err := os.ReadFile(identityPath)
			if err != nil || !bytes.Equal(keyBefore, keyAfter) {
				t.Fatal("recovery replaced the original identity key")
			}
			if count(t, `SELECT count(*) FROM agents`) != agentsBefore || count(t, `SELECT count(*) FROM agent_principals WHERE agent_id=?`, agentID) != principalsBefore {
				t.Fatal("recovery created an extra Agent or principal")
			}
			if !reflect.DeepEqual(originalAgent, h.agent(t, agentID)) || !reflect.DeepEqual(originalProfile, profile(t, agentID)) {
				t.Fatal("recovery changed profile, account binding, or acquisition")
			}
			if handoffCalls.Load() != 0 || count(t, `SELECT count(*) FROM console_v2_handoffs WHERE principal_id=?`, principalID) != handoffsBefore {
				t.Fatal("automatic recovery created a browser handoff")
			}
		})
	}
}
