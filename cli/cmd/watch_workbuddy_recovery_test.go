package cmd

import (
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkBuddyMigrationRecoversOnlyUnrecordedSellerOrders(t *testing.T) {
	for _, scenario := range []string{"recover", "paid", "recorded", "recorded_unknown", "recorded_failed", "recorded_completed", "foreign_seller", "finished", "bad_cursor", "lookup_failed", "detail_foreign", "identity_changed"} {
		t.Run(scenario, func(t *testing.T) {
			var watch *accountWatch
			detailCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/orders":
					if r.URL.Query().Get("role") != "seller" {
						t.Error("wrong order role")
					}
					if scenario == "lookup_failed" {
						http.Error(out, "failed", 500)
						return
					}
					if (r.URL.Query().Get("state") == "in_progress") != (scenario == "paid") {
						fmt.Fprint(out, `{"code":0,"data":{"orders":[],"next_cursor":""}}`)
						return
					}
					seller, next := "42", ""
					state := "awaiting_seller"
					if scenario == "paid" {
						state = "in_progress"
					}
					if scenario == "foreign_seller" {
						seller = "43"
					}
					if scenario == "bad_cursor" {
						next = "repeat"
					}
					if scenario == "identity_changed" {
						cred, _ := auth.LoadV2Credentials(watch.server.Name)
						cred.AgentID = "other"
						auth.SaveV2Credentials(watch.server.Name, cred)
					}
					fmt.Fprintf(out, `{"code":0,"data":{"orders":[{"order_id":"123","seller_agent_id":"%s","version":3,"state":"%s"}],"next_cursor":"%s"}}`, seller, state, next)
				case "/api/v1/orders/123":
					detailCalls++
					seller, state := "42", "awaiting_seller"
					if scenario == "paid" {
						state = "in_progress"
					}
					if scenario == "finished" {
						state = "completed"
					}
					if scenario == "detail_foreign" {
						seller = "43"
					}
					fmt.Fprintf(out, `{"code":0,"data":{"order":{"order_id":"123","buyer_agent_id":"41","seller_agent_id":"%s","version":3,"state":"%s","current_snapshot_id":"456"}}}`, seller, state)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(out, r)
				}
			}))
			defer server.Close()
			watch = newCommissionWatch(t, server.URL, "commission_order")
			settings, _ := config.Load()
			if err := settings.UpdateServerWithCommission(watch.server.Name, watch.server.Endpoint, watch.server.StreamEndpoint, watch.server.Endpoint); err != nil {
				t.Fatal(err)
			}
			watch.server.CommissionEndpoint = watch.server.Endpoint
			cred, _ := auth.LoadV2Credentials(watch.server.Name)
			cred.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
			auth.SaveV2Credentials(watch.server.Name, cred)
			if strings.HasPrefix(scenario, "recorded") {
				jobID, err := watch.journal.RecoverCommission("123", 2)
				if err != nil {
					t.Fatal(err)
				}
				if status := strings.TrimPrefix(scenario, "recorded_"); status != scenario {
					if _, _, err := watch.journal.NextKind("commission_order"); err != nil {
						t.Fatal(err)
					}
					if err := watch.journal.SetCommissionExecution(jobID, "123", 2, 0); err != nil {
						t.Fatal(err)
					}
					if err := watch.journal.Update(jobID, status, "existing_outcome", "", ""); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := recoverWorkBuddyOrders(context.Background(), *watch.binding)
			wantErr := scenario == "foreign_seller" || scenario == "bad_cursor" || scenario == "lookup_failed" || scenario == "detail_foreign" || scenario == "identity_changed"
			if (err != nil) != wantErr {
				t.Fatalf("err=%v", err)
			}
			if !wantErr {
				if err := recoverWorkBuddyOrders(context.Background(), *watch.binding); err != nil {
					t.Fatal(err)
				}
				jobs, err := dispatch.ReadJournalStatus(*watch.binding)
				if err != nil {
					t.Fatal(err)
				}
				want := 1
				if scenario == "finished" {
					want = 0
				}
				if len(jobs) != want {
					t.Fatalf("jobs=%+v", jobs)
				}
				if strings.HasPrefix(scenario, "recorded") && detailCalls != 0 {
					t.Fatal("replayed existing order")
				}
			}
		})
	}
}

// Test helper opens a fresh journal before any worker is running.
func recoverWorkBuddyOrders(ctx context.Context, b dispatch.Binding) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	srv, err := cfg.GetActive(b.Server)
	if err != nil {
		return err
	}
	cred, err := auth.LoadV2Credentials(b.Server)
	if err != nil {
		return err
	}
	q, err := dispatch.OpenJournal(b)
	if err != nil {
		return err
	}
	w := &accountWatch{home: b.Home, server: *srv, identity: *cred, scope: b.Scope, binding: &b, journal: q}
	return w.recoverUnrecordedWorkBuddyOrders(ctx)
}
