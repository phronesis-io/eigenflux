package commissionindex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"eigenflux_server/pkg/es"

	elasticsearch "github.com/elastic/go-elasticsearch/v8"
)

func TestBuildDocumentUsesIndependentVersionsAndNormalizedText(t *testing.T) {
	doc := BuildDocument(CatalogueSnapshot{CommissionID: 1, SellerAgentID: 2, Status: "active", CatalogueVersion: 4, Title: "  Build  API ", Tags: []string{"Go", "API"}}, StatisticsSnapshot{CommissionID: 1, StatisticsVersion: 7, CompletedCount: 2}, []float32{1})
	if !doc.Active || doc.CatalogueVersion != 4 || doc.StatisticsVersion != 7 || doc.SearchText != "Build API Go API" {
		t.Fatalf("unexpected document: %#v", doc)
	}
}

func TestTombstoneRetainsVersion(t *testing.T) {
	doc := Tombstone(CatalogueSnapshot{CommissionID: 1, CatalogueVersion: 3}, StatisticsSnapshot{StatisticsVersion: 4})
	if doc.Active || doc.CatalogueVersion != 3 || doc.StatisticsVersion != 4 {
		t.Fatalf("unexpected tombstone: %#v", doc)
	}
}

type commissionRoundTripFunc func(*http.Request) (*http.Response, error)

func (f commissionRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

func withCommissionESTransport(t *testing.T, roundTrip commissionRoundTripFunc) {
	t.Helper()
	previous := es.Client
	client, err := elasticsearch.NewClient(elasticsearch.Config{Transport: roundTrip})
	if err != nil {
		t.Fatal(err)
	}
	es.Client = client
	t.Cleanup(func() { es.Client = previous })
}

func TestESStoreGetUsesReadAliasAndExactInt64ID(t *testing.T) {
	body := &closeTrackingBody{Reader: strings.NewReader(`{"_index":"commissions-v1","_id":"9223372036854775807","found":true,"_source":{"commission_id":9223372036854775807,"active":true,"catalogue_version":12,"statistics_version":13,"title":"` + strings.Repeat("irrelevant", 1000) + `","embedding":[1,2,3]}}`)}
	withCommissionESTransport(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.EscapedPath() != "/commissions/_doc/9223372036854775807" {
			t.Fatalf("request=%s %s", req.Method, req.URL.EscapedPath())
		}
		if got := req.URL.Query().Get("_source_includes"); got != "commission_id,active,catalogue_version,statistics_version" {
			t.Fatalf("_source_includes=%q", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: body}, nil
	})

	doc, found, err := (ESStore{Index: "commissions-v1", Alias: "commissions"}).Get(context.Background(), 9223372036854775807)
	if err != nil || !found {
		t.Fatalf("Get() found=%v error=%v", found, err)
	}
	if doc.CommissionID != 9223372036854775807 || !doc.Active || doc.CatalogueVersion != 12 || doc.StatisticsVersion != 13 {
		t.Fatalf("unexpected document: %#v", doc)
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
}

func TestESStoreGetNotFound(t *testing.T) {
	body := &closeTrackingBody{Reader: strings.NewReader(`{"_index":"commissions-v1","_id":"42","found":false}`)}
	withCommissionESTransport(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: body}, nil
	})
	doc, found, err := (ESStore{Alias: "commissions"}).Get(context.Background(), 42)
	if err != nil || found || doc.CommissionID != 0 {
		t.Fatalf("Get() doc=%#v found=%v error=%v", doc, found, err)
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
}

func TestESStoreGetRejectsNonDocument404(t *testing.T) {
	for _, body := range []string{
		`{"error":{"type":"index_not_found_exception"},"status":404}`,
		`{"_index":"commissions-v1","_id":"41","found":false}`,
		`{"_index":"","_id":"42","found":false}`,
		`{"_index":"commissions-v1","_id":"42","found":true}`,
		`not-json`,
		strings.Repeat("x", maxCommissionGetResponseBytes+1),
	} {
		withCommissionESTransport(t, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		if _, _, err := (ESStore{Alias: "commissions"}).Get(context.Background(), 42); err == nil {
			t.Fatalf("non-document 404 accepted: %.80q", body)
		}
	}
}

func TestESStoreGetFailsClosed(t *testing.T) {
	t.Run("nonpositive ID", func(t *testing.T) {
		previous := es.Client
		es.Client = nil
		t.Cleanup(func() { es.Client = previous })
		if _, _, err := (ESStore{}).Get(context.Background(), 0); err == nil || len(err.Error()) > 96 {
			t.Fatalf("error=%v", err)
		}
	})

	for _, tc := range []struct {
		name      string
		status    int
		body      io.Reader
		transport error
	}{
		{"uninitialized", 0, nil, errors.New("dial secret-vector-[1,2,3]")},
		{"upstream status", http.StatusInternalServerError, strings.NewReader(strings.Repeat("private-source-vector", 1000)), nil},
		{"invalid source", http.StatusOK, strings.NewReader(`{"_source":{"commission_id":"not-an-id","embedding":[1,2,3]}}`), nil},
		{"trailing JSON", http.StatusOK, strings.NewReader(`{"_source":{"commission_id":42}} {"secret":"catalogue"}`), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withCommissionESTransport(t, func(*http.Request) (*http.Response, error) {
				if tc.transport != nil {
					return nil, tc.transport
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(tc.body)}, nil
			})
			_, _, err := (ESStore{Alias: "commissions"}).Get(context.Background(), 42)
			if err == nil {
				t.Fatal("Get() succeeded")
			}
			if len(err.Error()) > 96 || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "vector") || strings.Contains(err.Error(), "catalogue") {
				t.Fatalf("unsafe error=%q", err)
			}
		})
	}
}

func TestESStoreReadyReadsCommissionMappingDimensions(t *testing.T) {
	body := &closeTrackingBody{Reader: strings.NewReader(`{"commissions-v1":{"mappings":{"properties":{"embedding":{"type":"dense_vector","dims":768}}}}}`)}
	withCommissionESTransport(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.EscapedPath() != "/commissions/_mapping" {
			t.Fatalf("request=%s %s", req.Method, req.URL.EscapedPath())
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: body}, nil
	})
	dimensions, err := (ESStore{Alias: "commissions"}).Ready(context.Background())
	if err != nil || dimensions != 768 {
		t.Fatalf("Ready() dimensions=%d error=%v", dimensions, err)
	}
	if !body.closed {
		t.Fatal("mapping response body was not closed")
	}
}

func TestESStoreReadyFailsClosedForUnavailableOrInconsistentMapping(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"missing", http.StatusNotFound, `{"private":"source"}`},
		{"missing embedding", http.StatusOK, `{"commissions-v1":{"mappings":{"properties":{}}}}`},
		{"invalid dimensions", http.StatusOK, `{"commissions-v1":{"mappings":{"properties":{"embedding":{"type":"dense_vector","dims":0}}}}}`},
		{"inconsistent aliases", http.StatusOK, `{"commissions-v1":{"mappings":{"properties":{"embedding":{"type":"dense_vector","dims":768}}}},"commissions-v2":{"mappings":{"properties":{"embedding":{"type":"dense_vector","dims":1536}}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withCommissionESTransport(t, func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			_, err := (ESStore{Alias: "commissions"}).Ready(context.Background())
			if err == nil || len(err.Error()) > 96 || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "source") {
				t.Fatalf("unsafe error=%v", err)
			}
		})
	}
}

func TestESStoreSearchSetsJSONContentType(t *testing.T) {
	withCommissionESTransport(t, func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type=%q", got)
		}
		if got := req.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("Accept=%q", got)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"commission_id", "completion_rate_bps", "average_rating_milli", "has_rating", "completed_count"} {
			if !strings.Contains(string(body), `"`+field+`"`) {
				t.Fatalf("search request omitted required source field %q: %s", field, body)
			}
		}
		if strings.Contains(string(body), `"embedding"`) {
			t.Fatalf("search request returned embedding source: %s", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(`{"hits":{"hits":[]}}`))}, nil
	})
	if _, err := (ESStore{Alias: "commissions"}).Search(context.Background(), SearchRequest{Query: "integration", Limit: 1}); err != nil {
		t.Fatalf("Search() error=%v", err)
	}
}

func TestESStoreUpsertSetsJSONContentType(t *testing.T) {
	withCommissionESTransport(t, func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type=%q", got)
		}
		if got := req.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("Accept=%q", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(`{"result":"updated"}`))}, nil
	})
	if err := (ESStore{Alias: "commissions"}).Upsert(context.Background(), Document{CommissionID: 1}); err != nil {
		t.Fatalf("Upsert() error=%v", err)
	}
}

func TestESStoreEnsureAtomicallyMovesAliasToSingleWriteIndex(t *testing.T) {
	requestCount := 0
	withCommissionESTransport(t, func(req *http.Request) (*http.Response, error) {
		requestCount++
		var body []byte
		if req.Body != nil {
			var err error
			body, err = io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
		}
		switch requestCount {
		case 1:
			if req.Method != http.MethodPut || req.URL.EscapedPath() != "/commissions-v2" || strings.Contains(string(body), `"aliases"`) {
				t.Fatalf("create request=%s %s body=%s", req.Method, req.URL.EscapedPath(), body)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		case 2:
			if req.Method != http.MethodGet || req.URL.EscapedPath() != "/_alias/commissions" {
				t.Fatalf("alias check=%s %s", req.Method, req.URL.EscapedPath())
			}
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		case 3:
			text := string(body)
			if req.Method != http.MethodPost || req.URL.EscapedPath() != "/_aliases" ||
				!strings.Contains(text, `"remove":{"alias":"commissions","index":"*","must_exist":false}`) ||
				!strings.Contains(text, `"add":{"alias":"commissions","index":"commissions-v2","is_write_index":true}`) {
				t.Fatalf("alias request=%s %s body=%s", req.Method, req.URL.EscapedPath(), body)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		default:
			t.Fatalf("unexpected request %d: %s %s", requestCount, req.Method, req.URL.EscapedPath())
			return nil, errors.New("unexpected request")
		}
	})
	if err := (ESStore{Index: "commissions-v2", Alias: "commissions", Dimensions: 768}).Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure() error=%v", err)
	}
	if requestCount != 3 {
		t.Fatalf("request count=%d, want 3", requestCount)
	}
}

func TestESStoreEnsureDoesNotMoveExistingAlias(t *testing.T) {
	requestCount := 0
	withCommissionESTransport(t, func(req *http.Request) (*http.Response, error) {
		requestCount++
		switch requestCount {
		case 1:
			return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		case 2:
			if req.Method != http.MethodGet || req.URL.EscapedPath() != "/_alias/commissions" {
				t.Fatalf("alias check=%s %s", req.Method, req.URL.EscapedPath())
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(`{"commissions-v1":{}}`))}, nil
		default:
			t.Fatalf("Ensure moved an existing alias: %s %s", req.Method, req.URL.EscapedPath())
			return nil, errors.New("unexpected alias move")
		}
	})
	if err := (ESStore{Index: "commissions-v2", Alias: "commissions", Dimensions: 768}).Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure() error=%v", err)
	}
	if requestCount != 2 {
		t.Fatalf("request count=%d, want 2", requestCount)
	}
}

func TestESStoreSearchRejectsOversizedResponse(t *testing.T) {
	withCommissionESTransport(t, func(*http.Request) (*http.Response, error) {
		body := `{"hits":{"hits":[]},"private":"` + strings.Repeat("x", maxCommissionSearchResponseBytes) + `"}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	_, err := (ESStore{Alias: "commissions"}).Search(context.Background(), SearchRequest{Query: "integration", Limit: 1})
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("Search() error=%v", err)
	}
}
