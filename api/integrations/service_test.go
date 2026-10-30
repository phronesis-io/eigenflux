package integrations

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"eigenflux_server/pkg/integrationidentity"
	"eigenflux_server/pkg/integrationprofile"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type ids struct{ next int64 }

func (g *ids) NextID() (int64, error) { g.next++; return g.next, nil }

func TestManagedHTTPRegistrationAndLiveControls(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := database.DB()
	conn.SetMaxOpenConns(1)
	defer conn.Close()
	for _, q := range []string{
		`CREATE TABLE agents(agent_id INTEGER PRIMARY KEY,short_id TEXT,email TEXT UNIQUE,email_kind TEXT,agent_name TEXT,bio TEXT,created_at BIGINT,updated_at BIGINT)`,
		`CREATE UNIQUE INDEX short_ids ON agents(short_id) WHERE short_id IS NOT NULL`,
		`CREATE TABLE agent_profiles(agent_id INTEGER PRIMARY KEY,status INTEGER,updated_at BIGINT)`,
		`CREATE TABLE integration_control(id INTEGER PRIMARY KEY,enabled BOOLEAN,revision BIGINT,actor TEXT,reason TEXT,updated_at BIGINT)`,
		`INSERT INTO integration_control VALUES(1,false,1,'test','',0)`,
		`CREATE TABLE integration_audit(id INTEGER PRIMARY KEY,app_id TEXT,agent_id BIGINT,actor TEXT,operation TEXT,reason TEXT,occurred_at BIGINT)`,
	} {
		if err := database.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.AutoMigrate(&integrationidentity.App{}, &integrationidentity.Agent{}, &integrationidentity.Installation{}); err != nil {
		t.Fatal(err)
	}
	secret, hash, err := integrationidentity.NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&integrationidentity.App{AppID: "partner", Name: "Partner", Enabled: true, CredentialHash: hash, MaxAgents: 1, MaxInstallationsPerAgent: 1, MinPollIntervalSeconds: 3600, Revision: 1}).Error; err != nil {
		t.Fatal(err)
	}
	store := &integrationidentity.Store{DB: database, IDs: &ids{}}
	h := server.New()
	(&Service{Store: store}).Register(h)
	request := func(path, token, body string) (int, []byte) {
		var headers []ut.Header
		if token != "" {
			headers = append(headers, ut.Header{Key: "Authorization", Value: "Bearer " + token})
		}
		// Descriptive client headers must never bypass credential validation.
		headers = append(headers, ut.Header{Key: "X-SDK-Source", Value: "official"})
		r := ut.PerformRequest(h.Engine, "POST", path, &ut.Body{Body: strings.NewReader(body), Len: len(body)}, headers...).Result()
		return r.StatusCode(), append([]byte(nil), r.Body()...)
	}
	const path = "/api/v2/integrations/sdk/installations"
	const body = `{"external_agent_id":"one","runtime_name":"jarvis","runtime_version":"2.0","sdk_version":"1.0.0","runtime_instance_id":"first"}`
	if status, _ := request(path, "", body); status != 401 {
		t.Fatal("anonymous registration allowed", status)
	}
	if status, _ := request(path, "efv2a_personal", body); status != 401 {
		t.Fatal("personal token accepted as app credential", status)
	}
	if status, _ := request(path, secret, body); status != 403 {
		t.Fatal("global default-off ignored", status)
	}
	if err := store.SetEnabled(context.Background(), "", 0, true, "test-operator", "test start"); err != nil {
		t.Fatal(err)
	}
	if status, _ := request(path, secret, strings.TrimSuffix(body, "}")+`,"app_id":"other"}`); status != 400 {
		t.Fatal("body can override application", status)
	}
	status, raw := request(path, secret, body)
	if status != 200 {
		t.Fatalf("registration %d %s", status, raw)
	}
	var response struct {
		Data integrationidentity.RegistrationResult `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	registered := response.Data
	if registered.Agent.AgentID == 0 || registered.Installation.InstallationID == "" || registered.Agent.AppID != "partner" {
		t.Fatalf("binding incomplete %+v", registered)
	}
	if err := database.AutoMigrate(&integrationprofile.State{}); err != nil {
		t.Fatal(err)
	}
	contextRequest := func(method, instance, payload string) (int, []byte) {
		r := ut.PerformRequest(h.Engine, method, "/api/v2/integrations/agents/me/context", &ut.Body{Body: strings.NewReader(payload), Len: len(payload)},
			ut.Header{Key: "Authorization", Value: "Bearer " + secret},
			ut.Header{Key: "X-EigenFlux-Installation-ID", Value: registered.Installation.InstallationID},
			ut.Header{Key: "X-EigenFlux-Runtime-Instance-ID", Value: instance}).Result()
		return r.StatusCode(), append([]byte(nil), r.Body()...)
	}
	if status, raw := contextRequest("PUT", "first", `{"source_revision":1,"summary":"private-raw-context"}`); status != 202 || strings.Contains(string(raw), "private-raw-context") {
		t.Fatal(status, string(raw))
	}
	if status, raw := contextRequest("GET", "first", ""); status != 200 || strings.Contains(string(raw), "private-raw-context") {
		t.Fatal(status, string(raw))
	}
	if status, _ := contextRequest("PUT", "first", `{"source_revision":1,"summary":"conflicting"}`); status != 409 {
		t.Fatal("revision conflict accepted", status)
	}
	if status, _ := contextRequest("PUT", "first", `{"source_revision":2,"fields":{"runtime_name":"official"}}`); status != 400 {
		t.Fatal("protected field accepted", status)
	}
	if status, _ := contextRequest("PUT", "foreign-instance", `{"source_revision":2,"summary":"wrong runtime"}`); status != 403 {
		t.Fatal("foreign instance accepted", status)
	}
	controlBody, _ := json.Marshal(map[string]string{"installation_id": registered.Installation.InstallationID, "runtime_instance_id": "first"})
	if err := store.SetEnabled(context.Background(), "partner", registered.Agent.AgentID, false, "test-operator", "test stop"); err != nil {
		t.Fatal(err)
	}
	if status, _ := contextRequest("GET", "first", ""); status != 403 {
		t.Fatal("disabled context remains readable", status)
	}
	status, raw = request("/api/v2/integrations/sdk/control:check", secret, string(controlBody))
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	var control struct {
		Data integrationidentity.Policy `json:"data"`
	}
	if err := json.Unmarshal(raw, &control); err != nil {
		t.Fatal(err)
	}
	if control.Data.BusinessEnabled || control.Data.Reason != "AGENT_DISABLED" {
		t.Fatalf("disabled state not visible %+v", control.Data)
	}
	if status, _ := request(path, secret, body); status != 403 {
		t.Fatal("disabled Agent recreated", status)
	}
	if err := database.Exec("DROP TABLE integration_apps").Error; err != nil {
		t.Fatal(err)
	}
	if status, _ := request(path, secret, body); status != 503 {
		t.Fatal("storage failure allowed request", status)
	}
}
