package legacy

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
	"eigenflux_server/pkg/featureindex"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	elasticsearch "github.com/elastic/go-elasticsearch/v8"
)

type commissionSearchRoundTripFunc func(*http.Request) (*http.Response, error)

func (f commissionSearchRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSearchCommissionsByIDSkipsEmbedding(t *testing.T) {
	s := &Service{redis: redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})}
	t.Cleanup(func() { s.redis.Close() })
	if err := (featureindex.CommissionIndex{Redis: s.redis, IndexName: "commissions-v1"}).Write(context.Background(), featureindex.CommissionDocument{CommissionID: 42, CatalogueVersion: 1, Active: true, Title: "Free audit", PriceFen: 0, Currency: "CNY", PromisedDeliveryMS: 86400000}); err != nil {
		t.Fatal(err)
	}

	var embeddingCalls atomic.Int32
	embeddingServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		embeddingCalls.Add(1)
	}))
	defer embeddingServer.Close()

	s.cfg = &config.Config{
		CommissionIndexAlias: "commissions",
		EmbeddingProvider:    "openai",
		EmbeddingBaseURL:     embeddingServer.URL,
		EmbeddingModel:       "text-embedding-3-small",
		EmbeddingDimensions:  1536,
	}

	previousESClient := es.Client
	client, err := elasticsearch.NewClient(elasticsearch.Config{Transport: commissionSearchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(body), `"commission_id":42`) || strings.Contains(string(body), `"knn"`) {
			t.Fatalf("unexpected exact search request: %s", body)
		}
		if !strings.Contains(string(body), `"title"`) {
			t.Fatalf("search omitted display title: %s", body)
		}
		response := `{"hits":{"hits":[{"_index":"commissions-v1","_score":1,"_source":{"commission_id":42,"catalogue_version":1,"title":"Free audit"}}]}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	es.Client = client
	t.Cleanup(func() { es.Client = previousESClient })

	commissionID := int64(42)
	response, err := (s).SearchCommissions(context.Background(), &sortmodel.SearchCommissionsReq{CommissionId: &commissionID})
	if err != nil || response.BaseResp == nil || response.BaseResp.Code != 0 || len(response.Candidates) != 1 || response.Candidates[0].CommissionId != commissionID {
		t.Fatalf("response=%+v error=%v", response, err)
	}
	if embeddingCalls.Load() != 0 {
		t.Fatalf("exact ID lookup made %d embedding calls", embeddingCalls.Load())
	}
	candidate := response.Candidates[0]
	if candidate.GetTitle() != "Free audit" || candidate.PriceFen == nil || *candidate.PriceFen != 0 || candidate.GetCurrency() != "CNY" || candidate.GetPromisedDeliveryMs() != 86400000 {
		t.Fatalf("catalogue summary missing: %+v", candidate)
	}
	encoded := make([]byte, candidate.BLength())
	candidate.FastWriteNocopy(encoded, nil)
	var decoded sortmodel.CommissionCandidate
	if _, err := decoded.FastRead(encoded); err != nil || decoded.PriceFen == nil || *decoded.PriceFen != 0 || decoded.GetTitle() != candidate.GetTitle() || decoded.GetCurrency() != candidate.GetCurrency() || decoded.GetPromisedDeliveryMs() != candidate.GetPromisedDeliveryMs() {
		t.Fatalf("optional summary lost on RPC wire: %+v / %v", decoded, err)
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
