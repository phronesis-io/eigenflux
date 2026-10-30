package cmd

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
)

func TestLegacyWorkBuddyPMMigration(t *testing.T) {
	for _, changedIdentity := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "identity_changed"}[changedIdentity], func(t *testing.T) {
			w := newCommissionWatch(t, "https://example.test", "commission_order")
			b := *w.binding
			b.Host, b.Mode, b.TimeoutSeconds = "workbuddy", "acp", 300
			b.Command = []string{os.Args[0], "watch", "workbuddy-acp", "--tool", "Read", "--tool", "Write", "--tool", "Edit"}
			b.SkillsDir = installHeartbeatTestRules(t)
			if err := dispatch.WriteJSON(dispatch.BindingPath(b.Home, b.Server), b); err != nil {
				t.Fatal(err)
			}
			journal, err := dispatch.OpenJournal(b)
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.AddMessages([]byte(fmt.Sprintf(`{"messages":[{"msg_id":"pending","conv_id":"existing","sender_id":"peer","receiver_id":%q,"content":"pending"}]}`, b.AgentID))); err != nil {
				t.Fatal(err)
			}
			pending := journal.Snapshot()
			cfgPath := config.HomeDir() + "/config.json"
			beforeConfig := []byte(`{"kv":{"auto_reply_pm":"false"}}`)
			if err := os.WriteFile(cfgPath, beforeConfig, 0600); err != nil {
				t.Fatal(err)
			}
			original, _ := os.ReadFile(dispatch.BindingPath(b.Home, b.Server))
			updated, err := upgradeLegacyWorkBuddyPMBinding(b, func() error {
				if changedIdentity {
					return errors.New("identity changed")
				}
				return nil
			})
			if changedIdentity {
				if err == nil {
					t.Fatal("accepted changed identity")
				}
				after, _ := os.ReadFile(dispatch.BindingPath(b.Home, b.Server))
				if string(after) != string(original) {
					t.Fatal("changed binding after failure")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := b
			want.Events = []string{"pm_push", "commission_order"}
			if !reflect.DeepEqual(updated, want) {
				t.Fatalf("changed scope or adapter: %+v", updated)
			}
			reopened, err := dispatch.OpenJournal(updated)
			if err != nil || !reflect.DeepEqual(reopened.Snapshot(), pending) {
				t.Fatalf("pending work changed: %v", err)
			}
			afterConfig, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(beforeConfig) != string(afterConfig) {
				t.Fatal("changed owner settings")
			}
			if legacyWorkBuddyPMBinding(updated, "darwin") {
				t.Fatal("migration is not idempotent")
			}
			for _, platform := range []string{"windows", "linux"} {
				if legacyWorkBuddyPMBinding(b, platform) {
					t.Fatal("migrates another platform")
				}
			}
			custom := b
			custom.TimeoutSeconds = 600
			if legacyWorkBuddyPMBinding(custom, "darwin") {
				t.Fatal("migrates modern explicit selection")
			}
			if !legacyWorkBuddyPMBinding(b, "darwin") {
				t.Fatal("missed original adapter")
			}
		})
	}
}

func TestLegacyWorkBuddyPMMigrationRejectsConcurrentEdits(t *testing.T) {
	for _, timing := range []string{"before", "during"} {
		t.Run(timing, func(t *testing.T) {
			w := newCommissionWatch(t, "https://example.test", "commission_order")
			b := *w.binding
			b.SkillsDir = installHeartbeatTestRules(t)
			path := dispatch.BindingPath(b.Home, b.Server)
			if err := dispatch.WriteJSON(path, b); err != nil {
				t.Fatal(err)
			}
			changed := b
			changed.TimeoutSeconds++
			write := func() {
				if err := dispatch.WriteJSON(path, changed); err != nil {
					t.Fatal(err)
				}
			}
			if timing == "before" {
				write()
			}
			_, err := upgradeLegacyWorkBuddyPMBinding(b, func() error {
				if timing == "during" {
					write()
				}
				return nil
			})
			if err == nil {
				t.Fatal("overwrote concurrent edit")
			}
			got, err := dispatch.ReadBinding(b.Home, b.Server)
			if err != nil || !reflect.DeepEqual(got, changed) {
				t.Fatalf("edit lost: %+v %v", got, err)
			}
		})
	}
}
