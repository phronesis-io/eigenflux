package maintenance

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCommissionSharedWireContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "consolev2", "testdata", "commission_diagnostics_wire.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name  string          `json:"name"`
			Valid bool            `json:"valid"`
			Event json.RawMessage `json:"event"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			var e Event
			decoder := json.NewDecoder(bytes.NewReader(tc.Event))
			decoder.DisallowUnknownFields()
			err := decoder.Decode(&e)
			if err == nil {
				err = Validate(e)
			}
			if (err == nil) != tc.Valid {
				t.Fatalf("valid=%v error=%v", tc.Valid, err)
			}
		})
	}
}
