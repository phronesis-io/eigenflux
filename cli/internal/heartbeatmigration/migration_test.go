package heartbeatmigration

import (
	"reflect"
	"testing"
)

func inventory() Inventory {
	return Inventory{Complete: true, Tasks: []Task{
		{ID: "other", Name: "EigenFlux", Prompt: "unrelated", Owner: "other", Schedule: "daily", Status: "ACTIVE"},
		{ID: "owned", Name: "old name", Prompt: "old static heartbeat", Owner: "eigenflux", Home: "/stable/.eigenflux", Server: "eigenflux", Schedule: "every 17 minutes", Status: "PAUSED", ThreadID: "original-thread", Metadata: map[string]interface{}{"notify": false}},
	}}
}

func TestPromptOnlyMigrationPreservesPausedIdentityAndCadence(t *testing.T) {
	in := inventory()
	p, err := Build(in, "/stable/.eigenflux", "eigenflux", "dynamic launcher")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "update" || p.TaskID != "owned" {
		t.Fatalf("%+v", p)
	}
	want := inventory()
	want.Tasks[1].Prompt = "dynamic launcher"
	if !reflect.DeepEqual(p.After, want) || !reflect.DeepEqual(p.Before, in) {
		t.Fatal("migration changed more than prompt")
	}
	if err := Verify(p, want); err != nil {
		t.Fatal(err)
	}
	again, err := Build(want, "/stable/.eigenflux", "eigenflux", "dynamic launcher")
	if err != nil || again.Status != "current" {
		t.Fatalf("second run not idempotent: %+v %v", again, err)
	}
}

func TestMigrationRejectsAmbiguityAndIncompleteInventory(t *testing.T) {
	for _, kind := range []string{"partial", "duplicate", "unknown-home", "unknown-server", "same-id"} {
		t.Run(kind, func(t *testing.T) {
			in := inventory()
			switch kind {
			case "partial":
				in.Complete = false
			case "duplicate":
				x := in.Tasks[1]
				x.ID = "duplicate"
				in.Tasks = append(in.Tasks, x)
			case "unknown-home":
				in.Tasks[1].Home = ""
			case "unknown-server":
				in.Tasks[1].Server = ""
			case "same-id":
				in.Tasks[1].ID = in.Tasks[0].ID
			}
			if _, err := Build(in, "/stable/.eigenflux", "eigenflux", "launcher"); err == nil {
				t.Fatal("ambiguous inventory accepted")
			}
		})
	}
}

func TestMigrationReadbackRejectsCollateralChanges(t *testing.T) {
	p, err := Build(inventory(), "/stable/.eigenflux", "eigenflux", "launcher")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"duplicate", "deleted", "schedule", "status", "thread", "home", "other", "prompt"} {
		t.Run(kind, func(t *testing.T) {
			in := inventory()
			in.Tasks[1].Prompt = "launcher"
			switch kind {
			case "duplicate":
				x := in.Tasks[1]
				x.ID = "new"
				in.Tasks = append(in.Tasks, x)
			case "deleted":
				in.Tasks = in.Tasks[1:]
			case "schedule":
				in.Tasks[1].Schedule = "changed"
			case "status":
				in.Tasks[1].Status = "ACTIVE"
			case "thread":
				in.Tasks[1].ThreadID = "new-thread"
			case "home":
				in.Tasks[1].Home = "/new"
			case "other":
				in.Tasks[0].Prompt = "changed"
			case "prompt":
				in.Tasks[1].Prompt = "old"
			}
			if err := Verify(p, in); err == nil {
				t.Fatal("invalid readback accepted")
			}
		})
	}
}
