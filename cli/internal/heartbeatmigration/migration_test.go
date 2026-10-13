package heartbeatmigration

import (
	"encoding/json"
	"reflect"
	"testing"
)

func inventory() Inventory {
	return Inventory{Complete: true, Tasks: []Task{
		{ID: "other", Name: "EigenFlux", Prompt: "unrelated", Owner: "other", Schedule: "daily", Status: "ACTIVE"},
		{ID: "owned", Name: "old name", Prompt: "old static heartbeat", Owner: "eigenflux", Purpose: "heartbeat", Home: "/stable/.eigenflux", Server: "eigenflux", Schedule: "every 17 minutes", Status: "PAUSED", ThreadID: "original-thread", Metadata: map[string]interface{}{"notify": false}},
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
	if !reflect.DeepEqual(p.After.Tasks, want.Tasks[1:]) || !reflect.DeepEqual(p.Before.Tasks, in.Tasks[1:]) {
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
	for _, kind := range []string{"duplicate", "deleted", "schedule", "status", "thread", "home", "owner", "purpose", "server", "prompt"} {
		t.Run(kind, func(t *testing.T) {
			in := inventory()
			in.Tasks[1].Prompt = "launcher"
			switch kind {
			case "duplicate":
				x := in.Tasks[1]
				x.ID = "new"
				in.Tasks = append(in.Tasks, x)
			case "deleted":
				in.Tasks = in.Tasks[:1]
			case "schedule":
				in.Tasks[1].Schedule = "changed"
			case "status":
				in.Tasks[1].Status = "ACTIVE"
			case "thread":
				in.Tasks[1].ThreadID = "new-thread"
			case "home":
				in.Tasks[1].Home = "/new"
			case "owner":
				in.Tasks[1].Owner = "other"
			case "purpose":
				in.Tasks[1].Purpose = "publish"
			case "server":
				in.Tasks[1].Server = "other"
			case "prompt":
				in.Tasks[1].Prompt = "old"
			}
			if err := Verify(p, in); err == nil {
				t.Fatal("invalid readback accepted")
			}
		})
	}
}

func TestMigrationPurposeAndUnrelatedChanges(t *testing.T) {
	for _, purpose := range []string{"", "publish"} {
		in := inventory()
		business := in.Tasks[1]
		business.ID, business.Purpose = "business", purpose
		only := Inventory{Complete: true, Tasks: []Task{business}}
		p, err := Build(only, business.Home, business.Server, "launcher")
		if err != nil || p.Status != "missing" || len(p.Before.Tasks) != 0 || len(p.After.Tasks) != 0 {
			t.Fatalf("business task selected: %+v %v", p, err)
		}
		in.Tasks = append(in.Tasks, business)
		p, err = Build(in, business.Home, business.Server, "launcher")
		if err != nil || len(p.Before.Tasks) != 1 || len(p.After.Tasks) != 1 {
			t.Fatalf("target not isolated: %+v %v", p, err)
		}
		in.Tasks[1].Prompt = "launcher"
		in.Tasks[0].Prompt = "changed"
		in.Tasks[2].Schedule = "changed"
		if err := Verify(p, in); err != nil {
			t.Fatal(err)
		}
		in.Tasks = in.Tasks[1:2]
		if err := Verify(p, in); err != nil {
			t.Fatal(err)
		}
		other := in.Tasks[0]
		other.ID, other.Server = "other-scope", "other"
		in.Tasks = append(in.Tasks, other)
		if err := Verify(p, in); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrationEmptyMetadataRoundTrip(t *testing.T) {
	in := inventory()
	in.Tasks[1].Metadata = map[string]interface{}{}
	p, err := Build(in, in.Tasks[1].Home, in.Tasks[1].Server, "launcher")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var saved Plan
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	in.Tasks[1].Prompt = "launcher"
	if err := Verify(saved, in); err != nil {
		t.Fatal(err)
	}
	in.Tasks[1].Metadata = map[string]interface{}{"changed": true}
	if err := Verify(saved, in); err == nil {
		t.Fatal("changed metadata accepted")
	}
}
