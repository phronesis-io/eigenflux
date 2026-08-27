package skills

import (
	"slices"
	"testing"
)

func TestProdAllowlistIncludesCommissionSkill(t *testing.T) {
	want := []string{"ef-broadcast", "ef-commission", "ef-communication", "ef-profile"}
	if !slices.Equal(ProdAllowlist, want) {
		t.Fatalf("ProdAllowlist = %v, want %v", ProdAllowlist, want)
	}
}
