package consolev2

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/jackc/pgx/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"eigenflux_server/kitex_gen/eigenflux/base"
	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	"eigenflux_server/pkg/feedcontract"
)

type contextFeedClient struct {
	feedservice.Client
	metadata *string
	empty    bool
}

func (f *contextFeedClient) FetchFeed(context.Context, *feedrpc.FetchFeedReq, ...callopt.Option) (*feedrpc.FetchFeedResp, error) {
	source, summary := "pgc", "An infrastructure signal"
	items := []*feedrpc.FeedItem{{ItemId: 101, SourceType: &source, Summary: &summary}}
	if f.empty {
		items = nil
	}
	return &feedrpc.FetchFeedResp{Items: items, DiscoveryMetadata: f.metadata, BaseResp: &base.BaseResp{}}, nil
}

func TestFeedV2ControlContextPostgres(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for Feed V2 PostgreSQL contracts")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		t.Fatal("Feed V2 tests require a loopback PostgreSQL host")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	exec := func(sql string, args ...interface{}) {
		t.Helper()
		if err := tx.Exec(sql, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`CREATE TEMP TABLE agent_onboarding_v2 (agent_id BIGINT PRIMARY KEY, state TEXT, active_context_revision BIGINT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_feed_v2_settings (agent_id BIGINT PRIMARY KEY, poll_interval_seconds INT) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_context_revisions (agent_id BIGINT, revision BIGINT, compiled_context JSONB) ON COMMIT DROP`,
		`CREATE TEMP TABLE agent_feed_exposures (agent_id BIGINT, source_type TEXT, source_id BIGINT, content_class TEXT, author_agent_id BIGINT, context_revision BIGINT, first_seen_at BIGINT, last_seen_at BIGINT, seen_count INT, PRIMARY KEY (agent_id, source_type, source_id)) ON COMMIT DROP`,
	} {
		exec(statement)
	}
	exec(`INSERT INTO agent_context_revisions VALUES (42, 1, '{"context_revision":1,"intent_actions":[]}')`)
	baseline := `{"input_origin":"baseline","fallback_reason":"empty_agent_context"}`
	aligned := `{"input_origin":"agent_context"}`
	revision, staleRevision := int64(1), int64(0)
	for _, tc := range []struct {
		name     string
		state    string
		active   *int64
		applied  *int64
		metadata *string
		empty    bool
		mode     string
		delivery string
	}{
		{"completed baseline full request", "completed", &revision, nil, &baseline, false, "baseline", "none"},
		{"completed baseline applied request", "completed", &revision, &revision, &baseline, false, "baseline", "none"},
		{"completed baseline stale request", "completed", &revision, &staleRevision, &baseline, false, "baseline", "none"},
		{"completed baseline empty page", "completed", &revision, &revision, &baseline, true, "baseline", "none"},
		{"incomplete baseline", "in_progress", nil, nil, &baseline, false, "baseline", "none"},
		{"incomplete retained revision", "in_progress", &revision, nil, &baseline, false, "baseline", "none"},
		{"incomplete without discovery metadata", "in_progress", &revision, &revision, nil, false, "baseline", "none"},
		{"personalized full", "completed", &revision, nil, &aligned, false, "intent_aligned", "full"},
		{"personalized unchanged", "completed", &revision, &revision, &aligned, false, "intent_aligned", "unchanged"},
		{"personalized stale", "completed", &revision, &staleRevision, &aligned, false, "intent_aligned", "full"},
		{"personalized without discovery metadata", "completed", &revision, nil, nil, false, "intent_aligned", "full"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec(`TRUNCATE agent_onboarding_v2, agent_feed_exposures`)
			exec(`INSERT INTO agent_onboarding_v2 VALUES (42, ?, ?)`, tc.state, tc.active)
			svc := &Service{db: tx, feedClient: &contextFeedClient{metadata: tc.metadata, empty: tc.empty}}
			h := server.New()
			h.POST("/feed", func(ctx context.Context, c *app.RequestContext) {
				c.Set("agent_id", int64(42))
				svc.pullFeedV2(ctx, c)
			})
			status, payload, _ := performJSON(t, h, http.MethodPost, "/feed", pullFeedRequest{ContextRevisionApplied: tc.applied})
			if status != http.StatusOK {
				t.Fatalf("status=%d payload=%#v", status, payload)
			}
			data := responseData(t, payload)
			personalization := data["personalization"].(map[string]interface{})
			if personalization["mode"] != tc.mode || personalization["onboarding_state"] != tc.state || personalization["context_delivery"] != tc.delivery {
				t.Fatalf("unexpected personalization: %#v", personalization)
			}
			if data["output_contract"] != feedcontract.ForMode(tc.mode) {
				t.Fatal("Feed output contract does not match its effective mode")
			}
			if tc.mode == "baseline" {
				if personalization["context_revision"] != nil || data["control_context_snapshot"] != nil {
					t.Fatalf("baseline Feed contains formal control context: %#v", data)
				}
			} else if personalization["context_revision"] != float64(revision) || (data["control_context_snapshot"] != nil) != (tc.delivery == "full") {
				t.Fatalf("personalized context delivery changed: %#v", data)
			}
			items := data["items"].([]interface{})
			if tc.empty {
				if len(items) != 0 {
					t.Fatalf("empty Feed returned items: %#v", items)
				}
				return
			}
			if len(items) != 1 {
				t.Fatalf("Feed lost its source item: %#v", items)
			}
			item := items[0].(map[string]interface{})
			if (item["intent_match"] != nil) != (tc.mode == "intent_aligned") {
				t.Fatalf("intent matching does not match Feed mode: %#v", item)
			}
			var exposure struct {
				ContextRevision *int64
				SeenCount       int
			}
			if err := tx.Raw(`SELECT context_revision, seen_count FROM agent_feed_exposures WHERE agent_id=42 AND source_id=101`).Scan(&exposure).Error; err != nil {
				t.Fatal(err)
			}
			if exposure.SeenCount != 1 || (exposure.ContextRevision != nil) != (tc.mode == "intent_aligned") ||
				(exposure.ContextRevision != nil && *exposure.ContextRevision != revision) {
				t.Fatalf("exposure revision does not match Feed mode: %+v", exposure)
			}
			var active int64
			if err := tx.Raw(`SELECT COALESCE(active_context_revision,0) FROM agent_onboarding_v2 WHERE agent_id=42`).Scan(&active).Error; err != nil {
				t.Fatal(err)
			}
			if tc.active != nil && active != *tc.active {
				t.Fatal("Feed fallback changed the owner's active context")
			}
		})
	}
}
