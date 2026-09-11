package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sortmodel "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/es"

	elasticsearch "github.com/elastic/go-elasticsearch/v8"
)

type commissionSearchRoundTripFunc func(*http.Request) (*http.Response, error)

func (f commissionSearchRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSearchCommissionsByIDSkipsEmbedding(t *testing.T) {
	var embeddingCalls atomic.Int32
	embeddingServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		embeddingCalls.Add(1)
	}))
	defer embeddingServer.Close()

	previousConfig := cfg
	cfg = &config.Config{
		CommissionIndexAlias: "commissions",
		EmbeddingProvider:    "openai",
		EmbeddingBaseURL:     embeddingServer.URL,
		EmbeddingModel:       "text-embedding-3-small",
		EmbeddingDimensions:  1536,
	}
	t.Cleanup(func() { cfg = previousConfig })

	previousESClient := es.Client
	client, err := elasticsearch.NewClient(elasticsearch.Config{Transport: commissionSearchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(body), `"commission_id":42`) || strings.Contains(string(body), `"knn"`) {
			t.Fatalf("unexpected exact search request: %s", body)
		}
		response := `{"hits":{"hits":[{"_score":1,"_source":{"commission_id":42,"active":true}}]}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	es.Client = client
	t.Cleanup(func() { es.Client = previousESClient })

	commissionID := int64(42)
	response, err := (&SortServiceESImpl{}).SearchCommissions(context.Background(), &sortmodel.SearchCommissionsReq{CommissionId: &commissionID})
	if err != nil || response.BaseResp == nil || response.BaseResp.Code != 0 || len(response.Candidates) != 1 || response.Candidates[0].CommissionId != commissionID {
		t.Fatalf("response=%+v error=%v", response, err)
	}
	if embeddingCalls.Load() != 0 {
		t.Fatalf("exact ID lookup made %d embedding calls", embeddingCalls.Load())
	}
}

func TestCommissionSearchModeValidation(t *testing.T) {
	zero := int64(0)
	negative := int64(-1)
	positive := int64(42)
	for _, tc := range []struct {
		name    string
		req     *sortmodel.SearchCommissionsReq
		wantErr bool
	}{
		{name: "nil", wantErr: true},
		{name: "missing", req: &sortmodel.SearchCommissionsReq{}, wantErr: true},
		{name: "zero ID", req: &sortmodel.SearchCommissionsReq{CommissionId: &zero}, wantErr: true},
		{name: "negative ID", req: &sortmodel.SearchCommissionsReq{CommissionId: &negative}, wantErr: true},
		{name: "conflicting", req: &sortmodel.SearchCommissionsReq{Query: "research", CommissionId: &positive}, wantErr: true},
		{name: "query", req: &sortmodel.SearchCommissionsReq{Query: " research "}},
		{name: "ID", req: &sortmodel.SearchCommissionsReq{CommissionId: &positive}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := commissionSearchMode(tc.req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("commissionSearchMode() error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
