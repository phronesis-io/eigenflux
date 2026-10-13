package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/heartbeatmigration"
)

func TestMigrationPendingAndRetry(t *testing.T) {
	_, server := runtimeTestConfig(t, "http://127.0.0.1:1", true)
	oldFormat := formatFlag
	formatFlag = "json"
	t.Cleanup(func() { formatFlag = oldFormat })
	call := func(action string, input interface{}) (migrationRecord, error) {
		t.Helper()
		cmd, _, err := rootCmd.Find([]string{"heartbeat", "migrate", action})
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		cmd.SetIn(bytes.NewReader(b))
		if err := cmd.Flags().Set("stdin", "true"); err != nil {
			t.Fatal(err)
		}
		out, err := captureHeartbeatStdout(t, func() error { return cmd.RunE(cmd, nil) })
		var r migrationRecord
		if err == nil && action == "plan" {
			if err := json.Unmarshal([]byte(out), &r); err != nil {
				t.Fatal(err)
			}
		}
		return r, err
	}
	in := heartbeatmigration.Inventory{Complete: true, Tasks: []heartbeatmigration.Task{
		{ID: "heartbeat", Owner: "eigenflux", Purpose: "heartbeat", Home: config.HomeDir(), Server: server, Prompt: "old", Schedule: "2h", Status: "PAUSED", Metadata: map[string]interface{}{}},
		{ID: "business", Owner: "eigenflux", Purpose: "publish", Home: config.HomeDir(), Server: server, Prompt: "private business prompt"},
	}}
	host := clientMetaForServerName(server).Host
	path := migrationPendingPath(host)
	currentInput := in
	currentInput.Tasks = append([]heartbeatmigration.Task(nil), in.Tasks...)
	launcher, err := nativeHeartbeatLauncher(config.HomeDir(), server, "skill", "auto")
	if err != nil {
		t.Fatal(err)
	}
	currentInput.Tasks[0].Prompt = launcher
	if current, err := call("plan", currentInput); err != nil || current.Plan.Status != "current" {
		t.Fatalf("fresh current: %+v %v", current, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("current created a plan snapshot")
	}
	var r migrationRecord
	for i := 0; i < 4; i++ {
		var err error
		r, err = call("plan", in)
		if err != nil {
			t.Fatal(err)
		}
		files, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*migration-*.json"))
		if err != nil || len(files) != 1 {
			t.Fatalf("pending is unbounded: %v %v", files, err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("private business prompt")) {
			t.Fatal("saved unrelated task")
		}
	}
	if migrationPendingPath(host+"-other") == path {
		t.Fatal("host scopes collide")
	}
	verify := func(id string, actual heartbeatmigration.Inventory) error {
		_, err := call("verify", map[string]interface{}{"plan_id": id, "complete": actual.Complete, "tasks": actual.Tasks})
		return err
	}
	actual := r.Plan.After
	actual.Tasks[0].Metadata = map[string]interface{}{}
	if err := verify(r.ID, actual); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("bounded snapshot missing: %v", err)
	}
	if err := verify(r.ID, actual); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	actual.Tasks[0].Schedule = "changed"
	if err := verify(r.ID, actual); err == nil {
		t.Fatal("retry ignored fresh readback")
	}
	actual.Tasks[0].Schedule = "2h"
	duplicate := actual.Tasks[0]
	duplicate.ID = "duplicate"
	actual.Tasks = append(actual.Tasks, duplicate)
	if err := verify(r.ID, actual); err == nil {
		t.Fatal("retry accepted duplicate heartbeat")
	}
	actual.Tasks = actual.Tasks[:1]
	newPlan, err := call("plan", in)
	if err != nil {
		t.Fatal(err)
	}
	// With the old snapshot superseded, retry must use this host's receipt.
	otherHost := host + "-other"
	other := r
	other.ID, other.Host, other.Verified = "11111111111111111111111111111111", otherHost, time.Now()
	if err := saveMaintenance(migrationReceiptPath(otherHost), other); err != nil {
		t.Fatal(err)
	}
	if migrationReceiptPath(host) == migrationReceiptPath(otherHost) {
		t.Fatal("receipt scopes collide")
	}
	if err := verify(r.ID, actual); err != nil {
		t.Fatal(err)
	}
	if _, err := call("plan", actual); err != nil {
		t.Fatal(err)
	}
	if err := verify(newPlan.ID, newPlan.Plan.After); err != nil {
		t.Fatal("concurrent current invalidated pending verify:", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("retry removed newer pending:", err)
	}
	var pending migrationRecord
	if err := json.Unmarshal(b, &pending); err != nil || pending.ID != newPlan.ID {
		t.Fatal("new pending changed")
	}
	current, err := call("plan", actual)
	if err != nil || current.Plan.Status != "current" {
		t.Fatalf("current failed: %+v %v", current, err)
	}
	if now, err := os.ReadFile(path); err != nil || !bytes.Equal(now, b) {
		t.Fatal("current changed another pending plan")
	}
	if _, err := call("plan", actual); err != nil {
		t.Fatal(err)
	}
	if now, err := os.ReadFile(path); err != nil || !bytes.Equal(now, b) {
		t.Fatal("repeated current changed pending plan")
	}
	r.Created = time.Now().Add(-2 * time.Hour)
	r.Verified = time.Now()
	if err := saveMaintenance(migrationReceiptPath(host), r); err != nil {
		t.Fatal(err)
	}
	if err := verify(r.ID, actual); err == nil {
		t.Fatal("expired receipt accepted")
	}
	r.Created = time.Now()
	r.Home = "/wrong-home"
	if err := saveMaintenance(migrationReceiptPath(host), r); err != nil {
		t.Fatal(err)
	}
	if err := verify(r.ID, actual); err == nil {
		t.Fatal("wrong receipt identity accepted")
	}
	newPlan.Created = time.Now().Add(-2 * time.Hour)
	if err := saveMaintenance(path, newPlan); err != nil {
		t.Fatal(err)
	}
	if err := verify(newPlan.ID, actual); err == nil {
		t.Fatal("expired pending accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("expired verify removed shared snapshot")
	}
}
