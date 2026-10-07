package reqinfo

import (
	"context"
	"strings"
	"testing"

	"github.com/bytedance/gopkg/cloud/metainfo"
)

func TestClientFromContext_Model(t *testing.T) {
	ctx := metainfo.WithPersistentValue(context.Background(), KeyClientModel, "claude-opus-4-8")
	ci := ClientFromContext(ctx)
	if ci.Model != "claude-opus-4-8" {
		t.Fatalf("expected model claude-opus-4-8, got %q", ci.Model)
	}
	if got := ci.ToVars()["client_model"]; got != "claude-opus-4-8" {
		t.Fatalf("expected client_model var, got %q", got)
	}
}

func TestBioProvenanceFromContext(t *testing.T) {
	ctx := context.Background()
	ctx = metainfo.WithPersistentValue(ctx, KeyBioSource, "memory,session")
	ctx = metainfo.WithPersistentValue(ctx, KeyBioNote, "tightened focus to agent infra")

	p := BioProvenanceFromContext(ctx)
	if p.Source != "memory,session" {
		t.Fatalf("expected source memory,session, got %q", p.Source)
	}
	if p.Note != "tightened focus to agent infra" {
		t.Fatalf("expected note set, got %q", p.Note)
	}
}

func TestBioProvenanceFromContext_Empty(t *testing.T) {
	p := BioProvenanceFromContext(context.Background())
	if p.Source != "" || p.Note != "" {
		t.Fatalf("expected empty provenance, got %+v", p)
	}
}

func TestBoundedClientHeaders(t *testing.T) {
	values := map[string]string{
		"X-Client-Host":           strings.Repeat("h", 129),
		"X-Client-Mode":           "plugin",
		"X-Client-Model":          "claude-opus-4-8",
		"X-CLI-Ver":               "0.9.1",
		"X-Client-Plugin-Version": "2.0.0",
	}
	get := func(name string) string { return values[name] }
	got := BoundedClientHeaders(get)
	want := ClientHeaders{Host: strings.Repeat("h", 129), Mode: "plugin", Model: "claude-opus-4-8", CLIVersion: "0.9.1", PluginVersion: "2.0.0"}
	if got != want {
		t.Fatalf("headers = %+v, want %+v", got, want)
	}

	values["X-Client-Host"] = strings.Repeat("h", 130)
	values["X-Client-Mode"] = "bogus"
	values["X-Client-Model"] = strings.Repeat("m", 129)
	values["X-CLI-Ver"] = strings.Repeat("v", 129)
	values["X-Client-Plugin-Version"] = "2.0.0 beta"
	if got := BoundedClientHeaders(get); got != (ClientHeaders{}) {
		t.Fatalf("oversized or invalid headers = %+v, want all empty", got)
	}

	values["X-Client-Mode"] = "skill"
	if got := BoundedClientHeaders(get); got.Mode != "skill" {
		t.Fatalf("mode = %q, want skill", got.Mode)
	}
}
