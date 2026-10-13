package llm

import (
	"context"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGroundSummaryCorrectionMustPassIndependentSecondAudit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses []string
		want      string
		failed    bool
	}{
		{"faithful", []string{`{"faithful":true,"unsupported":[],"correction":""}`}, "Original summary", false},
		{"corrected", []string{`{"faithful":false,"unsupported":["clinical"],"correction":"The trial did not show a mortality benefit."}`, `{"faithful":true,"unsupported":[],"correction":""}`}, "The trial did not show a mortality benefit.", false},
		{"still unsupported", []string{`{"faithful":false,"unsupported":["clinical"],"correction":"A trial showed no benefit."}`, `{"faithful":false,"unsupported":["proved absence"],"correction":"Another correction"}`}, "", true},
		{"missing verdict", []string{`{"unsupported":[],"correction":""}`}, "", true},
		{"contradictory", []string{`{"faithful":true,"unsupported":["clinical"],"correction":""}`}, "", true},
		{"empty correction", []string{`{"faithful":false,"unsupported":["clinical"],"correction":""}`}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mocks := make([]*httptest.Server, len(tc.responses))
			for i, s := range tc.responses {
				mocks[i] = mockServer(t, s)
				defer mocks[i].Close()
			}
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i := calls
				calls++
				if i >= len(mocks) {
					t.Error("unexpected extra audit")
					w.WriteHeader(500)
					return
				}
				mocks[i].Config.Handler.ServeHTTP(w, r)
			}))
			defer srv.Close()
			got, err := newTestClient(t, srv.URL).GroundSummary(context.Background(), "The trial did not show a mortality benefit.", "", "Original summary")
			if tc.failed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.want, got)
			require.Equal(t, len(tc.responses), calls)
		})
	}
}
func TestGroundSummaryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := newTestClient(t, "http://127.0.0.1:1")
	_, err := c.GroundSummary(ctx, "Source", "", "Summary")
	require.Error(t, err)
}
