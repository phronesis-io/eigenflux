package consolev2

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
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
	if json.Unmarshal(body, &corpus) != nil || corpus.SchemaVersion != "agent_attention.v1" {
		t.Fatalf("invalid Attention golden corpus at %s", path)
	}
	return corpus
}

func TestAttentionGoldenCorpusMatchesServerValidators(t *testing.T) {
	corpus := loadAttentionGoldenCorpus(t)
	for _, test := range corpus.PublishCases {
		t.Run("publish/"+test.Name, func(t *testing.T) {
			var request attentionPublishRequest
			decoder := json.NewDecoder(bytes.NewReader(test.Payload))
			decoder.DisallowUnknownFields()
			err := decoder.Decode(&request)
			if err == nil {
				if trailingErr := decoder.Decode(&struct{}{}); !errors.Is(trailingErr, io.EOF) {
					err = trailingErr
				}
			}
			if err == nil {
				err = validateAttentionPublish(&request, time.Now().UnixMilli())
			}
			if (err == nil) != test.Valid {
				t.Fatalf("valid=%v, err=%v", test.Valid, err)
			}
		})
	}
	for _, test := range corpus.CompletionCases {
		t.Run("complete/"+test.Name, func(t *testing.T) {
			err := validateAttentionCommandResult(test.Result)
			if (err == nil) != test.Valid {
				t.Fatalf("valid=%v, err=%v", test.Valid, err)
			}
		})
	}
}

func TestAttentionSchemaEnumsMatchServer(t *testing.T) {
	path := filepath.Join("..", "..", "contracts", "agent_attention.v1.schema.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Extensions struct {
			PersistedProtocolVersion string   `json:"persisted_protocol_version"`
			PublishBodyMaxBytes      int      `json:"publish_body_max_bytes"`
			CustomFlagMaxUTF8Bytes   int      `json:"custom_flag_max_utf8_bytes"`
			ParticipationCategories  []string `json:"participation_categories"`
			FocusCategories          []string `json:"focus_categories"`
			SourceTypes              []string `json:"source_types"`
			ResultEntityTypes        []string `json:"result_entity_types"`
			ParticipationPresetFlags []string `json:"participation_preset_flags"`
			FocusPresetFlags         []string `json:"focus_preset_flags"`
		} `json:"x-eigenflux-contract"`
	}
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Extensions.PublishBodyMaxBytes != attentionPublishBodyLimit || schema.Extensions.CustomFlagMaxUTF8Bytes != 20 {
		t.Fatalf("schema limits drifted: %#v", schema.Extensions)
	}
	if schema.Extensions.PersistedProtocolVersion != attentionProtocolVersion {
		t.Fatalf("persisted protocol version=%q, want %q", schema.Extensions.PersistedProtocolVersion, attentionProtocolVersion)
	}
	assertAttentionBoolSet(t, schema.Extensions.ParticipationPresetFlags, participationActionFlags)
	assertAttentionBoolSet(t, schema.Extensions.FocusPresetFlags, focusActionFlags)
	assertAttentionBoolSet(t, schema.Extensions.ParticipationCategories, participationCategories)
	assertAttentionBoolSet(t, schema.Extensions.FocusCategories, focusCategories)
	assertAttentionBoolSet(t, schema.Extensions.SourceTypes, attentionSourceTypes)
	assertAttentionBoolSet(t, schema.Extensions.ResultEntityTypes, attentionCommandResultEntityTypes)
}

func assertAttentionBoolSet(t *testing.T, expected []string, actual map[string]bool) {
	t.Helper()
	got := make([]string, 0, len(actual))
	for value, enabled := range actual {
		if enabled {
			got = append(got, value)
		}
	}
	sort.Strings(got)
	want := append([]string(nil), expected...)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("contract set mismatch: got=%v want=%v", got, want)
	}
}
