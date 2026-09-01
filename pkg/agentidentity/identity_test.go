package agentidentity

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
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

func TestGetAndGetBatchExposeOptionalEnglishDisplayName(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE agents (
		agent_id INTEGER PRIMARY KEY, short_id TEXT, agent_name TEXT, agent_name_en TEXT, identity_state TEXT NOT NULL DEFAULT 'active'
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO agents (agent_id, short_id, agent_name, agent_name_en, identity_state)
		VALUES (1, 'AbCdE', '星图研究助手', ' Atlas Research Assistant ', 'active'),
		       (2, 'FgHiJ', '中文名', '', 'active'),
		       (3, 'KlMnO', '已废弃临时 Agent', '', 'recovered_temporary')`).Error; err != nil {
		t.Fatal(err)
	}

	one, err := Get(context.Background(), db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if one.DisplayName != "星图研究助手" || one.DisplayNameEn != "Atlas Research Assistant" {
		t.Fatalf("unexpected localized identity: %+v", one)
	}

	batch, err := GetBatch(context.Background(), db, []int64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if batch[1].DisplayNameEn != "Atlas Research Assistant" || batch[2].DisplayNameEn != "" {
		t.Fatalf("unexpected localized identity batch: %+v", batch)
	}
	if _, exists := batch[3]; exists {
		t.Fatalf("recovered temporary Agent leaked through batch lookup: %+v", batch[3])
	}
	if _, err := Get(context.Background(), db, 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("recovered temporary Agent lookup error = %v, want ErrNotFound", err)
	}
	if _, err := Lookup(context.Background(), db, "KlMnO"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("recovered temporary short ID lookup error = %v, want ErrNotFound", err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
