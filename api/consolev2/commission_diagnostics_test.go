package consolev2

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

type commissionDiagnosticsFixture struct {
	NowMS int64 `json:"now_ms"`
	Cases []struct {
		Name  string          `json:"name"`
		Valid bool            `json:"valid"`
		Event json.RawMessage `json:"event"`
	} `json:"cases"`
}

func loadCommissionDiagnosticsFixture(t *testing.T) commissionDiagnosticsFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/commission_diagnostics_wire.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture commissionDiagnosticsFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func detailedCommissionEvent(t *testing.T, name string) maintenanceEvent {
	t.Helper()
	for _, row := range loadCommissionDiagnosticsFixture(t).Cases {
		if row.Name == name {
			var event maintenanceEvent
			if err := json.Unmarshal(row.Event, &event); err != nil {
				t.Fatal(err)
			}
			event.EventAt = time.Now().UnixMilli()
			return event
		}
	}
	t.Fatalf("missing Commission diagnostics fixture %s", name)
	return maintenanceEvent{}
}

func TestCommissionDiagnosticsSharedWireContract(t *testing.T) {
	fixture := loadCommissionDiagnosticsFixture(t)
	for _, row := range fixture.Cases {
		t.Run(row.Name, func(t *testing.T) {
			var event maintenanceEvent
			decoder := json.NewDecoder(bytes.NewReader(row.Event))
			decoder.DisallowUnknownFields()
			err := decoder.Decode(&event)
			if err == nil {
				err = validateMaintenanceEvent(event, time.UnixMilli(fixture.NowMS))
			}
			if row.Valid != (err == nil) {
				t.Fatalf("valid=%t error=%v", row.Valid, err)
			}
			if !row.Valid {
				return
			}
			// Ingestion marshals the typed DTO into JSONB. Every accepted wire
			// field, including an explicit zero count/exit status, must survive.
			roundTrip, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			var original, encoded any
			if json.Unmarshal(row.Event, &original) != nil || json.Unmarshal(roundTrip, &encoded) != nil {
				t.Fatal("invalid wire round-trip")
			}
			if !reflect.DeepEqual(original, encoded) {
				t.Fatalf("accepted diagnostic fields were lost: %s", roundTrip)
			}
		})
	}
}

func TestCommissionDiagnosticsAllowlistsMatchCLIModule(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../../cli/internal/maintenance/commission_diagnostics.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"CommissionLocalCodes": commissionLocalCodes, "CommissionBridgeCodes": commissionBridgeCodes}
	for _, decl := range file.Decls {
		group, ok := decl.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, spec := range group.Specs {
			value := spec.(*ast.ValueSpec)
			if len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			expected, exists := want[value.Names[0].Name]
			if !exists {
				continue
			}
			literal, ok := value.Values[0].(*ast.BasicLit)
			if !ok {
				t.Fatal("CLI diagnostic allowlist must be a fixed string constant")
			}
			actual, err := strconv.Unquote(literal.Value)
			if err != nil || !reflect.DeepEqual(strings.Fields(actual), strings.Fields(expected)) {
				t.Fatalf("CLI/API diagnostic allowlist drift: %s", value.Names[0].Name)
			}
			delete(want, value.Names[0].Name)
		}
	}
	if len(want) != 0 {
		t.Fatal("CLI diagnostic allowlists were not found")
	}
}

func TestCommissionDiagnosticsHTTPRejectsPrivateOrUnknownFields(t *testing.T) {
	for _, row := range loadCommissionDiagnosticsFixture(t).Cases {
		if row.Valid {
			continue
		}
		t.Run(row.Name, func(t *testing.T) {
			var event map[string]any
			if err := json.Unmarshal(row.Event, &event); err != nil {
				t.Fatal(err)
			}
			event["event_at"] = time.Now().UnixMilli()
			svc := &Service{telemetryRates: map[string]telemetryRateState{}}
			h := server.New()
			h.POST("/test", func(_ context.Context, c *app.RequestContext) { c.Set("agent_id", int64(42)) }, svc.recordMaintenanceBatch)
			status, response, _ := performJSON(t, h, "POST", "/test", map[string]any{"events": []any{event}})
			if status != 400 {
				t.Fatalf("unsafe diagnostic reached persistence: status=%d", status)
			}
			body, _ := json.Marshal(response)
			for _, private := range []string{"secret-token-for-test", "/Users/private", "owner-private-session", "private prompt", "private-url"} {
				if bytes.Contains(body, []byte(private)) {
					t.Fatal("rejection response disclosed private input")
				}
			}
		})
	}
}

func TestCommissionDiagnosticsFixedCodeCoverage(t *testing.T) {
	for _, code := range strings.Fields(commissionLocalCodes) {
		e := detailedCommissionEvent(t, "missing_check_and_acp_rpc_error")
		e.Commission.LocalCode = code
		if err := validateMaintenanceEvent(e, time.Now()); err != nil {
			t.Fatalf("known local code %s: %v", code, err)
		}
	}
	for _, code := range strings.Fields(commissionBridgeCodes) {
		e := detailedCommissionEvent(t, "missing_check_and_acp_rpc_error")
		e.Commission.BridgeError = &commissionBridgeError{Code: code}
		if err := validateMaintenanceEvent(e, time.Now()); err != nil {
			t.Fatalf("known bridge code %s: %v", code, err)
		}
	}
}
