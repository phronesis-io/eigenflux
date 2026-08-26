package agentidentity

import (
	"bytes"
	"errors"
	"testing"
)

func TestGenerateShortIDUsesCaseSensitiveAlphabetAndRejectsBiasedBytes(t *testing.T) {
	// 208..255 must be rejected; the following accepted bytes map to A, Z,
	// a, z and A respectively.
	reader := bytes.NewReader([]byte{255, 208, 0, 25, 26, 51, 52, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	got, err := generateShortID(reader)
	if err != nil {
		t.Fatal(err)
	}
	if got != "AZazA" {
		t.Fatalf("got %q, want AZazA", got)
	}
}

func TestGenerateShortIDPropagatesEntropyFailure(t *testing.T) {
	_, err := generateShortID(errorReader{})
	if err == nil {
		t.Fatal("expected entropy error")
	}
}

func TestValidShortID(t *testing.T) {
	for _, value := range []string{"AbCdE", "abcde", "ABCDE"} {
		if !ValidShortID(value) {
			t.Fatalf("expected valid: %q", value)
		}
	}
	for _, value := range []string{"abcd", "abcdef", "abc1e", "Ａbcde", " abcde"} {
		if ValidShortID(value) {
			t.Fatalf("expected invalid: %q", value)
		}
	}
}

func TestDisplayName(t *testing.T) {
	if got := DisplayName("  Atlas  ", "AbCdE"); got != "Atlas" {
		t.Fatalf("got %q", got)
	}
	if got := DisplayName("", "AbCdE"); got != "Agent #AbCdE" {
		t.Fatalf("got %q", got)
	}
	if got := DisplayName("", ""); got != "Agent" {
		t.Fatalf("got %q", got)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
