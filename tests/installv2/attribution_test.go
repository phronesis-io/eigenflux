package installv2_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"eigenflux_server/api/consolev2"
	"eigenflux_server/api/install"
	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/config"
	sharedDB "eigenflux_server/pkg/db"
	"eigenflux_server/pkg/invite"
)

const testOrigin = "https://console.example.test"

// This suite exercises public HTTP handlers against a migrated PostgreSQL
// database. PG_DSN must point at an isolated test database; no RPC services,
// outbound email, ad callbacks, or production credentials are needed.
func TestV2InstallAttribution(t *testing.T) {
	h := newHarness(t)

	t.Run("bilibili_install_to_verified_identity", func(t *testing.T) {
		ref := h.mint(t, "bilibili", "")
		h.json(t, http.MethodPost, "/api/v1/install/copy", map[string]any{"ref": ref}, http.StatusOK)
		recorder := ut.PerformRequest(h.http.Engine, http.MethodGet, "/r/"+ref, nil)
		doc := string(recorder.Result().Body())
		if recorder.Result().StatusCode() != http.StatusOK ||
			!strings.Contains(doc, "Referral code: `"+ref+"`") ||
			!strings.Contains(doc, "https://cdn.eigenflux.ai/skills/latest/install.md") ||
			string(recorder.Result().Header.Peek("Cache-Control")) != "no-store" {
			t.Fatalf("join document did not carry ref: status=%d body=%s", recorder.Result().StatusCode(), recorder.Result().Body())
		}
		reported := h.json(t, http.MethodPost, "/api/v1/install/report", map[string]any{
			"ref": ref, "metadata": map[string]string{"os": "test", "arch": "test"},
		}, http.StatusOK)
		if reported["converted"] != true || reported["attribution"].(map[string]any)["channel"] != "bilibili" {
			t.Fatalf("unexpected install conversion: %#v", reported)
		}
		key := newKey(t)
		req := h.request(t, key, ref)
		identity := h.provision(t, key, req)
		agentID := identity["agent_id"].(string)
		before := h.agent(t, agentID)
		if before.AcquisitionChannel != "bilibili" || before.EmailKind != "internal_alias" || identity["created"] != true {
			t.Fatalf("new key identity missing acquisition before email binding: %+v response=%#v", before, identity)
		}
		h.bindEmail(t, identity)
		after := h.agent(t, agentID)
		if after.AcquisitionChannel != "bilibili" || after.EmailKind != "v2_bound" || after.EmailVerifiedAt <= 0 {
			t.Fatalf("email binding lost acquisition or identity: %+v", after)
		}
		var token install.Token
		if err := h.db.Where("token = ?", ref).First(&token).Error; err != nil {
			t.Fatal(err)
		}
		if token.Channel != "bilibili" || token.CopiedAt <= 0 || token.FetchedAt <= 0 || token.ReportedAt <= 0 || token.ReportCount != 1 {
			t.Fatalf("incomplete persisted install funnel: %+v", token)
		}
		// A later installation can reuse this key without replacing the original
		// channel, including after the internal alias becomes a verified email.
		replacement := h.request(t, key, h.mint(t, "google", ""))
		reused := h.provision(t, key, replacement)
		if reused["agent_id"] != agentID || reused["created"] != false || h.agent(t, agentID).AcquisitionChannel != "bilibili" {
			t.Fatalf("existing stable identity was reattributed: %#v", reused)
		}
	})

	t.Run("ref_is_signed_and_receipt_bound", func(t *testing.T) {
		key := newKey(t)
		req := h.request(t, key, h.mint(t, "bilibili", ""))
		body := signedBody(t, key, req)
		otherRef := h.mint(t, "google", "")
		body["ref"] = otherRef
		h.error(t, body, http.StatusUnauthorized, "INVALID_PROOF")
		first := h.provision(t, key, req)
		replay := h.provision(t, key, req)
		if first["agent_id"] != replay["agent_id"] || first["access_token"] != replay["access_token"] {
			t.Fatalf("exact retry did not return the persisted receipt: first=%#v replay=%#v", first, replay)
		}
		req.Ref = otherRef
		h.error(t, signedBody(t, key, req), http.StatusConflict, "PROVISION_IDEMPOTENCY_CONFLICT")
		if got := h.agent(t, first["agent_id"].(string)).AcquisitionChannel; got != "bilibili" {
			t.Fatalf("conflicting retry overwrote acquisition: %q", got)
		}
	})

	t.Run("absent_unknown_and_invalid_refs", func(t *testing.T) {
		for _, ref := range []string{"", install.NewToken()} {
			key := newKey(t)
			identity := h.provision(t, key, h.request(t, key, ref))
			if identity["created"] != true || h.agent(t, identity["agent_id"].(string)).AcquisitionChannel != "" {
				t.Fatalf("unattributed registration failed for ref=%q: %#v", ref, identity)
			}
			// A reused key cannot fill an empty acquisition field using a later
			// campaign, either; attribution belongs only to identity creation.
			reused := h.provision(t, key, h.request(t, key, h.mint(t, "bilibili", "")))
			if reused["created"] != false || h.agent(t, identity["agent_id"].(string)).AcquisitionChannel != "" {
				t.Fatalf("later campaign claimed an existing unattributed identity: %#v", reused)
			}
		}
		key := newKey(t)
		req := h.request(t, key, "not-a-ref")
		h.error(t, signedBody(t, key, req), http.StatusBadRequest, "INVALID_REF")
		req.Ref = ""
		h.provision(t, key, req)
	})

	t.Run("invite_relationship_survives_email_binding", func(t *testing.T) {
		inviterKey := newKey(t)
		inviterIdentity := h.provision(t, inviterKey, h.request(t, inviterKey, ""))
		inviterID := inviterIdentity["agent_id"].(string)
		inviter := h.agent(t, inviterID)
		legacyCode := invite.NewCode()
		if err := h.db.Exec(`INSERT INTO invite_codes (code, kind, agent_id, created_at)
			VALUES (?, 'kol', ?, ?)`, legacyCode, inviterID, time.Now().UnixMilli()).Error; err != nil {
			t.Fatal(err)
		}
		for _, code := range []string{inviter.ShortID, legacyCode} {
			t.Run(code, func(t *testing.T) {
				ref := h.mint(t, "bilibili", code)
				key := newKey(t)
				identity := h.provision(t, key, h.request(t, key, ref))
				agentID := identity["agent_id"].(string)
				before := h.agent(t, agentID)
				wantInviter, _ := strconv.ParseInt(inviterID, 10, 64)
				if before.AcquisitionChannel != "bilibili" || before.InvitedByCode != code || before.InviterAgentID != wantInviter || before.InvitedAt <= 0 {
					t.Fatalf("invitation not associated with provisioned identity: %+v", before)
				}
				h.bindEmail(t, identity)
				after := h.agent(t, agentID)
				if after.AcquisitionChannel != before.AcquisitionChannel || after.InvitedByCode != before.InvitedByCode || after.InviterAgentID != before.InviterAgentID || after.InvitedAt != before.InvitedAt {
					t.Fatalf("verified identity lost invitation: before=%+v after=%+v", before, after)
				}
			})
		}
	})

	t.Run("provision_failure_rolls_back_attribution", func(t *testing.T) {
		key := newKey(t)
		req := h.request(t, key, h.mint(t, "bilibili", ""))
		const callback = "installv2:fail-final-credential-insert"
		var failed atomic.Bool
		if err := h.db.Callback().Raw().Before("gorm:raw").Register(callback, func(tx *gorm.DB) {
			if strings.Contains(tx.Statement.SQL.String(), "INSERT INTO agent_credential_sessions") && failed.CompareAndSwap(false, true) {
				tx.AddError(errors.New("test failure after identity and attribution writes"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { h.db.Callback().Raw().Remove(callback) })
		h.json(t, http.MethodPost, "/api/v2/agent-identities/provision", signedBody(t, key, req), http.StatusServiceUnavailable)
		if !failed.Load() {
			t.Fatal("provision failed before the final credential insert; rollback was not exercised")
		}
		var principals int64
		if err := h.db.Raw(`SELECT COUNT(*) FROM agent_principals WHERE public_key = ?`, []byte(key.Public().(ed25519.PublicKey))).Scan(&principals).Error; err != nil || principals != 0 {
			t.Fatalf("failed provision persisted a principal: count=%d err=%v", principals, err)
		}
		var agents int64
		if err := h.db.Raw(`SELECT COUNT(*) FROM agents WHERE agent_name = ?`, req.AgentName).Scan(&agents).Error; err != nil || agents != 0 {
			t.Fatalf("failed provision persisted attributed Agent: count=%d err=%v", agents, err)
		}
		var grantStatus string
		if err := h.db.Raw(`SELECT status FROM agent_bootstrap_grants WHERE jti_hash = ?`, digest(req.BootstrapGrant)).Scan(&grantStatus).Error; err != nil || grantStatus != "issued" {
			t.Fatalf("failed provision consumed grant: status=%q err=%v", grantStatus, err)
		}
		identity := h.provision(t, key, req)
		if identity["created"] != true || h.agent(t, identity["agent_id"].(string)).AcquisitionChannel != "bilibili" {
			t.Fatalf("retry after rollback did not create an attributed Agent: %#v", identity)
		}
	})

	t.Run("legacy_upgrade_preserves_original_attribution", func(t *testing.T) {
		for _, originalChannel := range []string{"", "x"} {
			id, _ := h.ids.NextID()
			agentID := strconv.FormatInt(id, 10)
			shortID, err := agentidentity.GenerateShortID()
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UnixMilli() - 1000
			if err := h.db.Exec(`INSERT INTO agents (agent_id, short_id, email, agent_name, created_at, updated_at, acquisition_channel)
				VALUES (?, ?, ?, 'Legacy Attribution Test', ?, ?, ?)`, id, shortID, "legacy-"+agentID+"@installv2.example.test", now, now, originalChannel).Error; err != nil {
				t.Fatal(err)
			}
			if err := h.db.Exec(`INSERT INTO agent_profiles (agent_id, status, updated_at) VALUES (?, 0, ?)`, id, now).Error; err != nil {
				t.Fatal(err)
			}
			key := newKey(t)
			publicKey := base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
			grant := h.json(t, http.MethodPost, "/test/legacy-upgrade/"+agentID, map[string]any{
				"public_key": publicKey, "idempotency_key": "legacy-upgrade-" + agentID,
			}, http.StatusCreated)
			req := provisionProof{
				BootstrapGrant: grant["bootstrap_grant"].(string), Nonce: grant["nonce"].(string),
				PublicKey: publicKey, IdempotencyKey: "legacy-provision-" + agentID,
				IssuedAt: time.Now().UnixMilli(), AgentName: "Legacy Attribution Test", ExpectedAgentID: agentID,
				Draft: json.RawMessage(`{}`), Ref: h.mint(t, "bilibili", ""),
			}
			identity := h.provision(t, key, req)
			if identity["created"] != false || identity["agent_id"] != agentID || h.agent(t, agentID).AcquisitionChannel != originalChannel {
				t.Fatalf("legacy upgrade changed original attribution %q: %#v agent=%+v", originalChannel, identity, h.agent(t, agentID))
			}
		}
	})
}

type harness struct {
	db   *gorm.DB
	http *server.Hertz
	ids  *idGenerator
}

type idGenerator struct{ value atomic.Int64 }

func (g *idGenerator) NextID() (int64, error) { return g.value.Add(1), nil }

func newHarness(t *testing.T) *harness {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN must point at a migrated, isolated PostgreSQL test database")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	previous := sharedDB.DB
	sharedDB.DB = database
	t.Cleanup(func() { sharedDB.DB = previous })
	ids := &idGenerator{}
	ids.value.Store(time.Now().UnixMicro())
	svc, err := consolev2.NewService(database, ids, &config.Config{
		ConsoleV2BootstrapSecret:  "installv2-test-broker",
		ConsoleV2OTPPepper:        "installv2-test-otp-pepper",
		ConsoleV2PublicURL:        testOrigin,
		OfficialTestEmailSuffixes: []string{"@installv2.example.test"},
		OfficialTestOTP:           "135790",
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := server.New(server.WithHostPorts("127.0.0.1:0"))
	svc.Register(httpServer)
	// Supply the authenticated V1 identity at the middleware boundary while
	// exercising the public legacy-upgrade handler and its subject-bound grant.
	httpServer.POST("/test/legacy-upgrade/:agent_id", func(ctx context.Context, c *app.RequestContext) {
		id, err := strconv.ParseInt(c.Param("agent_id"), 10, 64)
		if err != nil {
			c.SetStatusCode(http.StatusBadRequest)
			return
		}
		c.Set("agent_id", id)
		svc.LegacyAgentUpgradeChallengeHandler()(ctx, c)
	})
	install.Register(httpServer, testOrigin)
	return &harness{db: database, http: httpServer, ids: ids}
}

func (h *harness) perform(t *testing.T, method, path string, body any, headers ...ut.Header) (int, map[string]any, [][]byte) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	headers = append(headers, ut.Header{Key: "Content-Type", Value: "application/json"},
		ut.Header{Key: "Origin", Value: testOrigin}, ut.Header{Key: "Host", Value: "console.example.test"})
	recorder := ut.PerformRequest(h.http.Engine, method, testOrigin+path,
		&ut.Body{Body: bytes.NewReader(encoded), Len: len(encoded)}, headers...)
	response := recorder.Result()
	var payload map[string]any
	if err := json.Unmarshal(response.Body(), &payload); err != nil {
		t.Fatalf("decode %s response: %v body=%s", path, err, response.Body())
	}
	return response.StatusCode(), payload, response.Header.PeekAll("Set-Cookie")
}

func (h *harness) json(t *testing.T, method, path string, body any, want int, headers ...ut.Header) map[string]any {
	t.Helper()
	status, payload, _ := h.perform(t, method, path, body, headers...)
	if status != want {
		t.Fatalf("%s %s status=%d want=%d response=%#v", method, path, status, want, payload)
	}
	data, _ := payload["data"].(map[string]any)
	return data
}

func (h *harness) mint(t *testing.T, channel, code string) string {
	t.Helper()
	data := h.json(t, http.MethodPost, "/api/v1/install/token", map[string]any{
		"utm_source": channel, "utm_medium": "video", "utm_campaign": "bilibili_v2_test", "invite_code": code,
	}, http.StatusOK)
	return data["ref"].(string)
}

// Field order and omission are part of the public V2 signing contract. This
// independent client deliberately does not call the server's private signer.
type provisionProof struct {
	BootstrapGrant  string            `json:"bootstrap_grant"`
	IdempotencyKey  string            `json:"idempotency_key"`
	Nonce           string            `json:"nonce"`
	PublicKey       string            `json:"public_key"`
	IssuedAt        int64             `json:"issued_at"`
	AgentName       string            `json:"agent_name"`
	ExpectedAgentID string            `json:"expected_agent_id,omitempty"`
	Draft           json.RawMessage   `json:"onboarding_draft,omitempty"`
	FieldProvenance map[string]string `json:"field_provenance,omitempty"`
	Ref             string            `json:"ref,omitempty"`
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (h *harness) request(t *testing.T, key ed25519.PrivateKey, ref string) provisionProof {
	t.Helper()
	id, _ := h.ids.NextID()
	publicKey := base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	grant := h.json(t, http.MethodPost, "/api/v2/bootstrap-grants", map[string]any{
		"public_key": publicKey, "entitlement_id": fmt.Sprintf("installv2-%d", id),
		"idempotency_key": fmt.Sprintf("installv2-grant-%d", id), "channel": "integration", "policy": "limited",
	}, http.StatusCreated, ut.Header{Key: "X-Bootstrap-Broker-Secret", Value: "installv2-test-broker"})
	return provisionProof{
		BootstrapGrant: grant["bootstrap_grant"].(string), Nonce: grant["nonce"].(string),
		IdempotencyKey: fmt.Sprintf("installv2-provision-%d", id), PublicKey: publicKey,
		IssuedAt: time.Now().UnixMilli(), AgentName: fmt.Sprintf("Install V2 Test %d", id), Draft: json.RawMessage(`{}`), Ref: ref,
	}
}

func signedBody(t *testing.T, key ed25519.PrivateKey, req provisionProof) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	proof := []byte("EF-AUTH-V2\x00POST\n/api/v2/agent-identities/provision\n" + digest(string(encoded)))
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	body["signature"] = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, proof))
	return body
}

func (h *harness) provision(t *testing.T, key ed25519.PrivateKey, req provisionProof) map[string]any {
	t.Helper()
	return h.json(t, http.MethodPost, "/api/v2/agent-identities/provision", signedBody(t, key, req), http.StatusOK)
}

func (h *harness) error(t *testing.T, body any, want int, code string) {
	t.Helper()
	status, payload, _ := h.perform(t, http.MethodPost, "/api/v2/agent-identities/provision", body)
	apiError, _ := payload["error"].(map[string]any)
	if status != want || apiError["code"] != code {
		t.Fatalf("provision status=%d want=%d error=%#v want code=%s", status, want, payload, code)
	}
}

type agentRow struct {
	AgentID            int64
	ShortID            string
	Email              string
	EmailKind          string
	EmailVerifiedAt    int64
	AcquisitionChannel string
	InvitedByCode      string
	InviterAgentID     int64
	InvitedAt          int64
}

func (h *harness) agent(t *testing.T, id string) agentRow {
	t.Helper()
	var result agentRow
	if err := h.db.Raw(`SELECT agent_id, short_id, email, email_kind, COALESCE(email_verified_at, 0) AS email_verified_at,
		acquisition_channel, invited_by_code, inviter_agent_id, invited_at FROM agents WHERE agent_id = ?`, id).Scan(&result).Error; err != nil {
		t.Fatal(err)
	}
	if result.AgentID == 0 {
		t.Fatalf("Agent %s was not persisted", id)
	}
	return result
}

func (h *harness) bindEmail(t *testing.T, identity map[string]any) {
	t.Helper()
	const nonce = "installv2-browser-nonce-1234567890"
	handoff := h.json(t, http.MethodPost, "/api/v2/console/handoffs", map[string]any{"browser_nonce": nonce},
		http.StatusCreated, ut.Header{Key: "Authorization", Value: "Bearer " + identity["access_token"].(string)})
	link, err := url.Parse(handoff["handoff_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	status, exchange, cookies := h.perform(t, http.MethodPost, "/api/v2/console/handoffs/exchange", map[string]any{
		"ticket": link.Query().Get("ticket"), "browser_nonce": nonce,
	})
	if status != http.StatusOK {
		t.Fatalf("handoff exchange status=%d payload=%#v", status, exchange)
	}
	var pairs []string
	for _, cookie := range cookies {
		pairs = append(pairs, strings.SplitN(string(cookie), ";", 2)[0])
	}
	headers := []ut.Header{
		{Key: "Cookie", Value: strings.Join(pairs, "; ")},
		{Key: "X-CSRF-Token", Value: exchange["data"].(map[string]any)["csrf_token"].(string)},
	}
	email := "agent-" + identity["agent_id"].(string) + "@installv2.example.test"
	challenge := h.json(t, http.MethodPost, "/api/v2/account-email-bindings/challenges", map[string]any{"email": email}, http.StatusAccepted, headers...)
	verified := h.json(t, http.MethodPost, "/api/v2/account-email-bindings/verify", map[string]any{
		"challenge_id": challenge["challenge_id"], "email": email, "otp": "135790",
	}, http.StatusOK, headers...)
	if verified["bound"] != true || verified["verification_level"] != "email_verified" {
		t.Fatalf("email binding incomplete: %#v", verified)
	}
	var bindings int64
	if err := h.db.Raw(`SELECT COUNT(*) FROM agent_email_bindings WHERE agent_id = ? AND normalized_email = ?
		AND verification_state = 'verified' AND status = 'active'`, identity["agent_id"], email).Scan(&bindings).Error; err != nil || bindings != 1 {
		t.Fatalf("verified email does not belong to the provisioned identity: count=%d err=%v", bindings, err)
	}
}
