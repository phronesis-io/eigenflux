package index

import (
	"strings"
	"testing"
)

func TestHierarchyAndMapping(t *testing.T) {
	v, err := Load(strings.NewReader(`{"version":"v1","embedding_version":"test","categories":[{"id":"design","name":"Design"}],"subtypes":[{"id":"web","name":"Web","category":"design"}],"intents":[{"id":"landing","name":"landing page","category":"design","subtype":"web","embedding":[1,0]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !v.Intent("landing", "design", "web") || v.Intent("landing", "wrong", "") {
		t.Fatal("parent validation")
	}
	if got := v.Search("unmapped", "design", "web", []float32{0, 1}, .8, 10); len(got) != 0 {
		t.Fatal("forced nearest mapping")
	}
	if got := v.Search("landing page", "design", "web", nil, .8, 10); len(got) != 1 || got[0].Version != "v1" {
		t.Fatal(got)
	}
	if got := v.Search("similar", "design", "web", []float32{1, 0}, .8, 10); len(got) != 1 {
		t.Fatal(got)
	}
}

func TestEmbeddingCompatibility(t *testing.T) {
	v := Vocabulary{EmbeddingVersion: "m1", Intents: []Node{{ID: "x", Vector: []float32{1, 0}}}}
	if v.ValidateEmbedding("m1", 2) != nil {
		t.Fatal("matching model rejected")
	}
	if v.ValidateEmbedding("m2", 2) == nil || v.ValidateEmbedding("m1", 3) == nil {
		t.Fatal("incompatible vectors accepted")
	}
}
