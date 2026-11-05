package consolev2

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"eigenflux_server/api/commissionaccess"
	"eigenflux_server/pkg/featureindex"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/jackc/pgx/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type marketServicesSource struct {
	rows      []featureindex.CommissionCatalogueSnapshot
	stats     map[int64]featureindex.CommissionStatisticsSnapshot
	err       error
	loop      bool
	listCalls int
	statCalls int
}

func (f *marketServicesSource) ListActiveIndexSnapshots(_ context.Context, cursor int64, limit int) ([]featureindex.CommissionCatalogueSnapshot, int64, error) {
	f.listCalls++
	if f.err != nil {
		return nil, 0, f.err
	}
	if f.loop {
		return f.rows[:1], 1, nil
	}
	start := int(cursor)
	end := start + limit
	if end >= len(f.rows) {
		return f.rows[start:], 0, nil
	}
	return f.rows[start:end], int64(end), nil
}
func (f *marketServicesSource) BatchGetStatistics(_ context.Context, ids []int64) ([]featureindex.CommissionStatisticsSnapshot, error) {
	f.statCalls++
	if len(ids) > marketSourcePageSize {
		return nil, errors.New("unbounded statistics batch")
	}
	rows := make([]featureindex.CommissionStatisticsSnapshot, 0, len(ids))
	for _, id := range ids {
		if s, ok := f.stats[id]; ok {
			rows = append(rows, s)
		}
	}
	return rows, nil
}
func (f *marketServicesSource) GetIndexSnapshot(context.Context, int64) (featureindex.CommissionCatalogueSnapshot, error) {
	panic("market must use bulk published snapshots")
}
func (f *marketServicesSource) GetStatistics(context.Context, int64) (featureindex.CommissionStatisticsSnapshot, error) {
	panic("market must batch statistics")
}

func marketFixture(t *testing.T) (*browserSessionFixture, *marketServicesSource) {
	t.Helper()
	f := newBrowserSessionFixture(t)
	f.add(t, 0, 10, false)
	f.add(t, 1, 20, false)
	for _, statement := range []string{
		`CREATE TABLE agent_onboarding_v2 (agent_id INTEGER, state TEXT, current_step INTEGER)`,
		`INSERT INTO agent_onboarding_v2 VALUES (10,'completed',5),(20,'completed',5)`,
		`CREATE TABLE agent_settings (agent_id INTEGER PRIMARY KEY, model TEXT)`,
		`CREATE TABLE user_relations (from_uid INTEGER,to_uid INTEGER,rel_type INTEGER)`,
		`INSERT INTO agents VALUES (21,'active','中文作者','English Provider','Alpha'),(22,'active','未知模型','','Beta'),(23,'active','其他作者','Other Provider','Gamma'),(24,'deleted','Removed','','Delta')`,
		`INSERT INTO agent_settings VALUES (21,'gpt-6'),(23,'claude')`,
		`INSERT INTO user_relations VALUES (10,21,1),(20,23,1),(10,23,2)`,
	} {
		if err := f.service.db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := &marketServicesSource{stats: map[int64]featureindex.CommissionStatisticsSnapshot{}}
	for id := int64(1); id <= 220; id++ {
		provider := int64(21)
		if id%2 == 0 {
			provider = 23
		}
		s.rows = append(s.rows, featureindex.CommissionCatalogueSnapshot{CommissionID: id, SellerAgentID: provider, Status: "active", Title: "报告 " + strconv.FormatInt(id, 10), CapabilityDescription: "research service", PriceFen: 10, Currency: "CNY", PromisedDeliveryMS: 300000})
		s.stats[id] = featureindex.CommissionStatisticsSnapshot{CommissionID: id, SellerAgentID: provider, CompletedCount: id / 2, AverageDeliveryMS: 120000, HasRating: true, AverageRatingMilli: 4500}
	}
	return f, s
}

func marketTestRouter(t *testing.T, f *browserSessionFixture, source featureindex.CommissionSource, allowed string) *server.Hertz {
	t.Helper()
	access, err := commissionaccess.New(true, allowed)
	if err != nil {
		t.Fatal(err)
	}
	h := server.New()
	f.service.RegisterMarketServices(h, source, access.ConsoleMiddleware())
	return h
}

func readMarket(t *testing.T, h *server.Hertz, path, cookie string) marketServicesResponse {
	t.Helper()
	recorder := ut.PerformRequest(h.Engine, http.MethodGet, "https://console.example.test/api/v2/console/market/services"+path, nil,
		ut.Header{Key: "Cookie", Value: cookie}, ut.Header{Key: "Host", Value: "console.example.test"}, ut.Header{Key: "Origin", Value: "https://console.example.test"})
	resp := recorder.Result()
	if resp.StatusCode() != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode(), resp.Body())
	}
	var envelope struct {
		Data marketServicesResponse `json:"data"`
	}
	if err := json.Unmarshal(resp.Body(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestMarketServicesCompleteCatalogueAndPagination(t *testing.T) {
	f, source := marketFixture(t)
	h := marketTestRouter(t, f, source, "10,20")
	first := readMarket(t, h, "?limit=100", f.cookieHeader(0))
	if len(first.Items) != 100 || first.Total != 220 || first.Items[0].CommissionID != "220" || first.NextCursor == "" || source.listCalls != 3 || source.statCalls != 3 {
		t.Fatalf("incomplete directory/page: %+v calls=%d/%d", first, source.listCalls, source.statCalls)
	}
	seen := map[string]bool{}
	data := first
	for {
		for _, item := range data.Items {
			if seen[item.CommissionID] {
				t.Fatalf("duplicate page entry %s", item.CommissionID)
			}
			seen[item.CommissionID] = true
		}
		if data.NextCursor == "" {
			break
		}
		data = readMarket(t, h, "?limit=100&cursor="+url.QueryEscape(data.NextCursor), f.cookieHeader(0))
	}
	if len(seen) != 220 || len(data.Items) != 20 {
		t.Fatalf("pagination lost results: %d final=%d", len(seen), len(data.Items))
	}
	filtered := readMarket(t, h, "?q="+url.QueryEscape("报告 219"), f.cookieHeader(0))
	if filtered.Total != 1 || filtered.Items[0].CommissionID != "219" {
		t.Fatalf("search ignored later catalogue page: %+v", filtered)
	}
}

func TestMarketServicesCombinedFiltersAndViewerRelations(t *testing.T) {
	f, source := marketFixture(t)
	h := marketTestRouter(t, f, source, "10,20")
	filters := "?q=english%20provider&model=gpt-6&friends_only=true&max_delivery_ms=120000&min_rating_milli=4500&has_completed=true&min_price_fen=10&max_price_fen=10"
	data := readMarket(t, h, filters, f.cookieHeader(0))
	if data.Total != 109 || len(data.Models) != 2 || data.Models[0] != "claude" || data.Models[1] != "gpt-6" {
		t.Fatalf("wrong combined filters/facets: %+v", data)
	}
	for _, item := range data.Items {
		if item.Provider.AgentID != "21" || !item.Provider.IsFriend || item.Provider.DisplayNameEn != "English Provider" {
			t.Fatalf("wrong provider: %+v", item)
		}
	}
	otherViewer := readMarket(t, h, filters, f.cookieHeader(1))
	if otherViewer.Total != 0 {
		t.Fatalf("friend filter leaked other viewer relations: %+v", otherViewer)
	}
	description := readMarket(t, h, "?q=RESEARCH", f.cookieHeader(0))
	if description.Total != 220 {
		t.Fatalf("description search failed: %+v", description)
	}
}

func TestMarketServicesPriceRangesAcrossCataloguePages(t *testing.T) {
	f, source := marketFixture(t)
	for i := range source.rows {
		source.rows[i].PriceFen = source.rows[i].CommissionID
	}
	source.rows[219].PriceFen = 0
	source.rows[217].PriceFen = math.MaxInt64
	h := marketTestRouter(t, f, source, "10")
	for _, tc := range []struct {
		name, query string
		ids         string
	}{
		{"free on final source page", "?max_price_fen=0", "220"},
		{"inclusive two-sided bounds", "?min_price_fen=100&max_price_fen=102", "102,101,100"},
		{"upper bound alone", "?max_price_fen=2", "220,2,1"},
		{"lower bound alone", "?min_price_fen=219", "219,218"},
		{"int64 upper boundary", "?min_price_fen=9223372036854775807&max_price_fen=9223372036854775807", "218"},
		{"no matches", "?min_price_fen=221&max_price_fen=222", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := source.listCalls
			data := readMarket(t, h, tc.query, f.cookieHeader(0))
			ids := make([]string, 0, len(data.Items))
			for _, item := range data.Items {
				ids = append(ids, item.CommissionID)
			}
			if strings.Join(ids, ",") != tc.ids || data.Total != len(ids) || data.NextCursor != "" || strings.Join(data.Models, ",") != "claude,gpt-6" || source.listCalls-before != 3 {
				t.Fatalf("incorrect global price filter: %+v source calls=%d", data, source.listCalls-before)
			}
		})
	}
	if data := readMarket(t, h, "?min_price_fen=0&max_price_fen=9223372036854775807", f.cookieHeader(0)); data.Total != 220 {
		t.Fatalf("nonnegative int64 range lost services: %+v", data)
	}
}

func TestMarketServicesPriceRangePagination(t *testing.T) {
	f, source := marketFixture(t)
	for i := range source.rows {
		source.rows[i].PriceFen = source.rows[i].CommissionID
	}
	h := marketTestRouter(t, f, source, "10")
	query := "?min_price_fen=99&max_price_fen=103&limit=2"
	var ids []string
	cursor := ""
	for page := 0; page < 3; page++ {
		data := readMarket(t, h, query+"&cursor="+url.QueryEscape(cursor), f.cookieHeader(0))
		if data.Total != 5 || len(data.Items) == 0 || len(data.Items) > 2 {
			t.Fatalf("incorrect filtered count/page: %+v", data)
		}
		for _, item := range data.Items {
			ids = append(ids, item.CommissionID)
			if item.PriceFen < 99 || item.PriceFen > 103 {
				t.Fatalf("out-of-range service reached page: %+v", item)
			}
		}
		cursor = data.NextCursor
		if (page < 2) != (cursor != "") {
			t.Fatalf("incorrect filtered cursor at page %d: %+v", page, data)
		}
	}
	if strings.Join(ids, ",") != "103,102,101,100,99" {
		t.Fatalf("filtered pagination lost or duplicated services: %v", ids)
	}
}

func TestMarketServicesUnknownEvidenceAndActiveProviders(t *testing.T) {
	f, source := marketFixture(t)
	source.rows = []featureindex.CommissionCatalogueSnapshot{
		{CommissionID: 1, SellerAgentID: 22, Status: "active", Title: "Unknown service"},
		{CommissionID: 2, SellerAgentID: 24, Status: "active", Title: "Deleted provider"},
	}
	source.stats = map[int64]featureindex.CommissionStatisticsSnapshot{
		1: {CommissionID: 1, SellerAgentID: 22, AverageRatingMilli: 5000, AverageDeliveryMS: 1},
		2: {CommissionID: 2, SellerAgentID: 24, CompletedCount: 4, HasRating: true, AverageRatingMilli: 5000},
	}
	h := marketTestRouter(t, f, source, "10")
	data := readMarket(t, h, "", f.cookieHeader(0))
	if data.Total != 1 || data.Items[0].Provider.Model != "" || len(data.Models) != 0 {
		t.Fatalf("unknown model or inactive identity changed: %+v", data)
	}
	for _, query := range []string{"?model=gpt-6", "?max_delivery_ms=300000", "?min_rating_milli=0", "?has_completed=true", "?friends_only=true"} {
		if data := readMarket(t, h, query, f.cookieHeader(0)); data.Total != 0 {
			t.Fatalf("unknown evidence passed %s: %+v", query, data)
		}
	}
}

func TestMarketServicesErrorsAndAuthentication(t *testing.T) {
	f, source := marketFixture(t)
	h := marketTestRouter(t, f, source, "10")
	for _, tc := range []struct {
		name, query, cookie, code string
		status                    int
	}{
		{name: "anonymous", status: 401, code: "CONSOLE_SESSION_REQUIRED"},
		{name: "allowlist", cookie: f.cookieHeader(1), status: 403, code: "COMMISSION_ACCESS_FORBIDDEN"},
		{name: "bad cursor", query: "?cursor=invalid", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "negative cursor", query: "?cursor=" + base64.RawURLEncoding.EncodeToString([]byte("-1:1")), cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "bad limit", query: "?limit=101", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "bad rating", query: "?min_rating_milli=5001", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "bad duration", query: "?max_delivery_ms=-1", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "negative minimum price", query: "?min_price_fen=-1", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "negative maximum price", query: "?max_price_fen=-1", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "negative zero price", query: "?min_price_fen=-0", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "reversed price bounds", query: "?min_price_fen=11&max_price_fen=10", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "minimum price overflow", query: "?min_price_fen=9223372036854775808", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "maximum price overflow", query: "?max_price_fen=9223372036854775808", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "fractional price", query: "?min_price_fen=0.1", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "exponent price", query: "?max_price_fen=1e2", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "signed price", query: "?min_price_fen=%2B1", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "nonnumeric price", query: "?max_price_fen=free", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "bad boolean", query: "?friends_only=yes", cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
		{name: "oversized query", query: "?q=" + strings.Repeat("x", 513), cookie: f.cookieHeader(0), status: 400, code: "MARKET_QUERY_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, payload, _ := performJSON(t, h, http.MethodGet, "/api/v2/console/market/services"+tc.query, nil, ut.Header{Key: "Cookie", Value: tc.cookie})
			if status != tc.status || responseErrorCode(t, payload) != tc.code {
				t.Fatalf("status=%d payload=%#v", status, payload)
			}
		})
	}
	if source.listCalls != 0 {
		t.Fatal("rejected request reached Commission")
	}
	for _, failure := range []string{"transport", "missing statistics", "loop", "invalid publication", "database"} {
		t.Run(failure, func(t *testing.T) {
			f, source := marketFixture(t)
			switch failure {
			case "transport":
				source.err = errors.New("private RPC error")
			case "missing statistics":
				delete(source.stats, 1)
			case "loop":
				source.loop = true
			case "invalid publication":
				source.rows[0].Status = "draft"
			case "database":
				f.service.db.Exec("DROP TABLE agent_settings")
			}
			h := marketTestRouter(t, f, source, "10")
			status, payload, _ := performJSON(t, h, http.MethodGet, "/api/v2/console/market/services", nil, ut.Header{Key: "Cookie", Value: f.cookieHeader(0)})
			if status != 503 || responseErrorCode(t, payload) != "MARKET_SERVICES_UNAVAILABLE" || strings.Contains(payload["error"].(map[string]interface{})["message"].(string), "private") {
				t.Fatalf("failure silently returned catalogue: status=%d payload=%#v", status, payload)
			}
		})
	}
}

func TestMarketServicesPostgresHTTP(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required for market PostgreSQL/HTTP contract")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		t.Fatal("market tests require loopback PostgreSQL")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	for _, statement := range []string{
		`CREATE TEMP TABLE agents (agent_id BIGINT PRIMARY KEY, identity_state TEXT, agent_name TEXT, agent_name_en TEXT, short_id TEXT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_settings (agent_id BIGINT PRIMARY KEY, model TEXT) ON COMMIT DROP`,
		`CREATE TEMP TABLE user_relations (from_uid BIGINT,to_uid BIGINT,rel_type SMALLINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_principals (principal_id BIGINT PRIMARY KEY, agent_id BIGINT, status TEXT, revoked_at BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_onboarding_v2 (agent_id BIGINT, state TEXT, current_step SMALLINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE console_v2_sessions (session_id TEXT PRIMARY KEY, agent_id BIGINT, principal_id BIGINT, session_secret_hash TEXT, csrf_secret_hash TEXT, scopes TEXT[], idle_expires_at BIGINT, absolute_expires_at BIGINT, last_seen_at BIGINT, auth_method TEXT, recent_auth_at BIGINT, status TEXT, revoked_at BIGINT) ON COMMIT DROP`,
		`INSERT INTO agents VALUES (10,'active','Buyer','','Buyer'),(21,'active','作者','English Author','Alpha')`,
		`INSERT INTO agent_settings VALUES (21,'gpt-6')`,
		`INSERT INTO user_relations VALUES (10,21,1)`,
		`INSERT INTO agent_principals VALUES (1,10,'active',NULL)`,
		`INSERT INTO agent_onboarding_v2 VALUES (10,'completed',5)`,
	} {
		if err := tx.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixMilli()
	if err := tx.Exec(`INSERT INTO console_v2_sessions VALUES ('market-session',10,1,?,?,'{}',?,?,?,'email_otp',NULL,'active',NULL)`, hashString("secret"), hashString("csrf"), now+3600000, now+7200000, now).Error; err != nil {
		t.Fatal(err)
	}
	const id = int64(9223372036854775000)
	source := &marketServicesSource{rows: []featureindex.CommissionCatalogueSnapshot{{CommissionID: id, SellerAgentID: 21, Status: "active", Title: "Research", PriceFen: 10, Currency: "CNY"}}, stats: map[int64]featureindex.CommissionStatisticsSnapshot{id: {CommissionID: id, SellerAgentID: 21, CompletedCount: 2, HasRating: true, AverageRatingMilli: 4500, AverageDeliveryMS: 1000}}}
	f := &browserSessionFixture{service: &Service{db: tx}}
	h := marketTestRouter(t, f, source, "10")
	data := readMarket(t, h, "?q=english&friends_only=true&model=gpt-6&min_rating_milli=4500&max_delivery_ms=1000&min_price_fen=10&max_price_fen=10", consoleCookieName+"=market-session.secret")
	if data.Total != 1 || data.Items[0].CommissionID != "9223372036854775000" || !data.Items[0].Provider.IsFriend {
		t.Fatalf("PostgreSQL contract mismatch: %+v", data)
	}
}
