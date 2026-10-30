package dal

import (
	"context"
	"eigenflux_server/pkg/es"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeleteItemRequiresCompletedRefreshedDeletion(t *testing.T) {
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{`{"deleted":1,"failures":[]}`, true},
		{`{"timed_out":true,"failures":[]}`, false},
		{`{"version_conflicts":1,"failures":[]}`, false},
		{`{"failures":[{"cause":"unavailable"}]}`, false},
		{`not json`, false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "true", r.URL.Query().Get("refresh"))
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Elastic-Product", "Elasticsearch")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			old := es.Client
			defer func() { es.Client = old }()
			var err error
			es.Client, err = elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{s.URL}, DisableRetry: true})
			require.NoError(t, err)
			err = DeleteItem(context.Background(), 1)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
