package main

import (
	"reflect"
	"testing"
)

func TestParseInt64CSV(t *testing.T) {
	got, err := parseInt64CSV("5, 3, 5, 9")
	if err != nil {
		t.Fatalf("parseInt64CSV returned error: %v", err)
	}
	want := []int64{3, 5, 9}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseInt64CSV=%v, want %v", got, want)
	}
}

func TestParseInt16CSV(t *testing.T) {
	got, err := parseInt16CSV("3,1,3")
	if err != nil {
		t.Fatalf("parseInt16CSV returned error: %v", err)
	}
	want := []int16{1, 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseInt16CSV=%v, want %v", got, want)
	}
}

func TestOptionsValidate(t *testing.T) {
	err := (options{workers: 8}).validate()
	if err == nil {
		t.Fatal("expected validate to fail without --all or --agent-ids")
	}

	err = (options{all: true, workers: 0}).validate()
	if err == nil {
		t.Fatal("expected validate to fail for workers=0")
	}

	err = (options{all: true, workers: 8}).validate()
	if err != nil {
		t.Fatalf("expected validate success, got %v", err)
	}
}

func TestBuildCachedProfile(t *testing.T) {
	profile := buildCachedProfile(7, []string{"ai-agents", "market-signals"}, "Singapore")
	if profile.AgentID != 7 {
		t.Fatalf("AgentID=%d, want 7", profile.AgentID)
	}
	if !reflect.DeepEqual(profile.Keywords, []string{"ai-agents", "market-signals"}) {
		t.Fatalf("Keywords=%v", profile.Keywords)
	}
	if !reflect.DeepEqual(profile.Domains, []string{"ai-agents", "market-signals"}) {
		t.Fatalf("Domains=%v", profile.Domains)
	}
	if profile.Geo != "" {
		t.Fatalf("Geo=%q, want empty", profile.Geo)
	}
	if profile.GeoCountry != "Singapore" {
		t.Fatalf("GeoCountry=%q, want Singapore", profile.GeoCountry)
	}
}
