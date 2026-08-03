package cmd

import (
	"strings"
	"testing"
)

func TestReadProfilePatchJSONBoundsStdin(t *testing.T) {
	valid := `{"seeking":["AI infra"]}`
	got, err := readProfilePatchJSON(strings.NewReader(valid), "-")
	if err != nil || string(got) != valid {
		t.Fatalf("valid stdin patch = (%q, %v)", got, err)
	}
	tooLarge := strings.Repeat("x", maxProfilePatchBytes+1)
	if _, err := readProfilePatchJSON(strings.NewReader(tooLarge), "-"); err == nil {
		t.Fatal("oversized stdin patch was accepted")
	}
}
