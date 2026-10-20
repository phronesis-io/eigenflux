package installv2_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/network/standard"

	"eigenflux_server/api/consolev2"
	"eigenflux_server/api/install"
	"eigenflux_server/pkg/config"
)

// This test uses the built CLI in separate processes, a real HTTP listener,
// production V2 handlers, and PostgreSQL. It verifies that delayed onboarding
// consumes the installer's persisted ref with a mutually valid signed proof.
func TestCLIInstallRefToV2Identity(t *testing.T) {
	binary := os.Getenv("EIGENFLUX_TEST_CLI")
	if binary == "" {
		t.Skip("EIGENFLUX_TEST_CLI must point at the CLI built from this checkout")
	}
	h := newHarness(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + listener.Addr().String()
	svc, err := consolev2.NewService(h.db, h.ids, &config.Config{
		ConsoleV2BootstrapSecret: "installv2-test-broker",
		ConsoleV2OTPPepper:       "installv2-test-otp-pepper",
		ConsoleV2PublicURL:       testOrigin,
	})
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	live := server.New(server.WithListener(listener), server.WithTransport(standard.NewTransporter))
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

	runCLI := func(home string, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append([]string{"--homedir", home, "--format", "json"}, args...)...)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		data, err := command.Output()
		if err != nil {
			t.Fatalf("CLI %v: %v stderr=%s stdout=%s", args, err, stderr.String(), data)
		}
		return data
	}
	decode := func(data []byte) map[string]any {
		t.Helper()
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("decode CLI: %v output=%s", err, data)
		}
		return result
	}
	provision := func(home string, ref string) map[string]any {
		t.Helper()
		identity := decode(runCLI(home, "agent", "init"))
		id, _ := h.ids.NextID()
		grant := h.json(t, http.MethodPost, "/api/v2/bootstrap-grants", map[string]any{
			"entitlement_id": fmt.Sprintf("cli-install-%d", id), "idempotency_key": fmt.Sprintf("cli-install-grant-%d", id),
			"public_key": identity["public_key"],
		}, http.StatusCreated, ut.Header{Key: "X-Bootstrap-Broker-Secret", Value: "installv2-test-broker"})
		args := []string{"agent", "provision", "--bootstrap-grant", grant["bootstrap_grant"].(string),
			"--nonce", grant["nonce"].(string), "--no-handoff"}
		if ref != "" {
			args = append(args, "--ref", ref)
		}
		return decode(runCLI(home, args...))
	}

	home := t.TempDir()
	runCLI(home, "server", "update", "--name", "eigenflux", "--endpoint", endpoint)
	ref := h.mint(t, "bilibili", "")
	saved := decode(runCLI(home, "agent", "install-ref", "--ref", ref, "--endpoint", endpoint))
	if saved["ref"] != ref || saved["saved"] != true {
		t.Fatalf("installer ref not saved: %#v", saved)
	}
	created := provision(home, "")
	agentID := created["agent_id"].(string)
	if created["created"] != true || h.agent(t, agentID).AcquisitionChannel != "bilibili" {
		t.Fatalf("delayed CLI provision lost the saved ref: %#v", created)
	}
	var installed install.Token
	if err := h.db.Where("token = ?", ref).First(&installed).Error; err != nil {
		t.Fatal(err)
	}
	if created["install_reported"] != true || installed.Status != "installed" || installed.ReportCount != 1 || installed.ReportedAt <= 0 {
		t.Fatalf("CLI did not repair missing install report: response=%#v token=%+v", created, installed)
	}
	otherRef := h.mint(t, "google", "")
	reused := provision(home, otherRef)
	if reused["agent_id"] != agentID || reused["created"] != false || h.agent(t, agentID).AcquisitionChannel != "bilibili" {
		t.Fatalf("repeated CLI provision changed acquisition: %#v", reused)
	}

	var repeated, untouched install.Token
	if err := h.db.Where("token = ?", ref).First(&repeated).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Where("token = ?", otherRef).First(&untouched).Error; err != nil {
		t.Fatal(err)
	}
	if reused["install_reported"] != true || repeated.ReportCount != 2 || repeated.ReportedAt != installed.ReportedAt || untouched.ReportCount != 0 {
		t.Fatalf("repeat report changed conversion/ref: repeated=%+v untouched=%+v", repeated, untouched)
	}

	directHome := t.TempDir()
	runCLI(directHome, "server", "update", "--name", "eigenflux", "--endpoint", endpoint)
	directRef := h.mint(t, "bilibili", "")
	direct := provision(directHome, directRef)
	if direct["created"] != true || h.agent(t, direct["agent_id"].(string)).AcquisitionChannel != "bilibili" {
		t.Fatalf("explicit CLI ref did not reach V2 identity: %#v", direct)
	}
	var directToken install.Token
	if err := h.db.Where("token = ?", directRef).First(&directToken).Error; err != nil {
		t.Fatal(err)
	}
	if direct["install_reported"] != true || directToken.Status != "installed" || directToken.ReportCount != 1 {
		t.Fatalf("explicit ref not reported: %+v", directToken)
	}
}
