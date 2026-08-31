package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type attentionGoldenCorpus struct {
	SchemaVersion string `json:"schema_version"`
	PublishCases  []struct {
		Name    string          `json:"name"`
		Valid   bool            `json:"valid"`
		Payload json.RawMessage `json:"payload"`
	} `json:"publish_cases"`
	CompletionCases []struct {
		Name   string          `json:"name"`
		Valid  bool            `json:"valid"`
		Result json.RawMessage `json:"result"`
	} `json:"completion_cases"`
}

func loadAttentionGoldenCorpus(t *testing.T) attentionGoldenCorpus {
	t.Helper()
	path := filepath.Join("..", "..", "contracts", "agent_attention.v1.golden.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Attention golden corpus: %v", err)
	}
	var corpus attentionGoldenCorpus
	if json.Unmarshal(body, &corpus) != nil || corpus.SchemaVersion != attentionSchemaVersion {
		t.Fatalf("invalid Attention golden corpus at %s", path)
	}
	return corpus
}

func TestAttentionGoldenCorpusMatchesCLIValidators(t *testing.T) {
	corpus := loadAttentionGoldenCorpus(t)
	for _, test := range corpus.PublishCases {
		t.Run("publish/"+test.Name, func(t *testing.T) {
			_, err := readAttentionPublishRequest(strings.NewReader(string(test.Payload)))
			if (err == nil) != test.Valid {
				t.Fatalf("valid=%v, err=%v", test.Valid, err)
			}
		})
	}
	for _, test := range corpus.CompletionCases {
		t.Run("complete/"+test.Name, func(t *testing.T) {
			_, err := parseRuntimeCommandResultForType(string(test.Result), attentionResponseCommandType)
			if (err == nil) != test.Valid {
				t.Fatalf("valid=%v, err=%v", test.Valid, err)
			}
		})
	}
}

func TestAttentionSchemaEnumsMatchCLI(t *testing.T) {
	path := filepath.Join("..", "..", "contracts", "agent_attention.v1.schema.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Extensions struct {
			PersistedProtocolVersion string   `json:"persisted_protocol_version"`
			PublishBodyMaxBytes     int      `json:"publish_body_max_bytes"`
			CustomFlagMaxUTF8Bytes  int      `json:"custom_flag_max_utf8_bytes"`
			ParticipationCategories []string `json:"participation_categories"`
			FocusCategories         []string `json:"focus_categories"`
			SourceTypes             []string `json:"source_types"`
			ResultEntityTypes       []string `json:"result_entity_types"`
			ParticipationPresetFlag []string `json:"participation_preset_flags"`
			FocusPresetFlag         []string `json:"focus_preset_flags"`
		} `json:"x-eigenflux-contract"`
	}
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Extensions.PublishBodyMaxBytes != attentionBodyLimit || schema.Extensions.CustomFlagMaxUTF8Bytes != attentionCustomFlagLimit {
		t.Fatalf("schema limits drifted: %#v", schema.Extensions)
	}
	if schema.Extensions.PersistedProtocolVersion != attentionSchemaVersion {
		t.Fatalf("persisted protocol version=%q, want %q", schema.Extensions.PersistedProtocolVersion, attentionSchemaVersion)
	}
	assertAttentionStringSet(t, schema.Extensions.ParticipationPresetFlag, attentionPresetFlags["participation"])
	assertAttentionStringSet(t, schema.Extensions.FocusPresetFlag, attentionPresetFlags["focus"])
	assertAttentionStringSet(t, schema.Extensions.ParticipationCategories, attentionCategories["participation"])
	assertAttentionStringSet(t, schema.Extensions.FocusCategories, attentionCategories["focus"])
	assertAttentionStringSet(t, schema.Extensions.SourceTypes, attentionSourceTypes)
	assertAttentionStringSet(t, schema.Extensions.ResultEntityTypes, attentionRuntimeResultEntityTypes)
}

func assertAttentionStringSet(t *testing.T, expected []string, actual map[string]struct{}) {
	t.Helper()
	got := make([]string, 0, len(actual))
	for value := range actual {
		got = append(got, value)
	}
	sort.Strings(got)
	want := append([]string(nil), expected...)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("contract set mismatch: got=%v want=%v", got, want)
	}
}
