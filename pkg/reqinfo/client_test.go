package reqinfo

import (
	"context"
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
