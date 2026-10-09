package cmd

import (
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
	"github.com/spf13/cobra"
)

func TestWatchCommissionOrderRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, state, seller string
		subscribed, wantErr bool
	}{
		{"paid", "in_progress", "42", true, false}, {"accept", "awaiting_seller", "42", true, false},
		{"buyer", "in_progress", "43", true, true}, {"finished", "completed", "42", true, true},
		{"unpaid", "pending_payment", "42", true, true}, {"disabled", "in_progress", "42", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/orders/123" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				fmt.Fprintf(w, `{"code":0,"data":{"order":{"order_id":"123","buyer_agent_id":"41","seller_agent_id":"%s","version":3,"state":"%s","current_snapshot_id":"456"}}}`, tc.seller, tc.state)
			}))
			defer server.Close()
			events := []string{"pm_push"}
			if tc.subscribed {
				events = append(events, "commission_order")
			}
			w := newCommissionWatch(t, server.URL, events...)
			settings, _ := config.Load()
			if err := settings.UpdateServerWithCommission(w.server.Name, w.server.Endpoint, w.server.StreamEndpoint, w.server.Endpoint); err != nil {
				t.Fatal(err)
			}
			credentials, _ := auth.LoadV2Credentials(w.server.Name)
			credentials.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
			if err := auth.SaveV2Credentials(w.server.Name, credentials); err != nil {
				t.Fatal(err)
			}
			cfg := &cobra.Command{}
			cfg.SetContext(context.Background())
			err := recoverWatchCommission(cfg, *w.binding, w.journal, "123")
			if (err != nil) != tc.wantErr {
				t.Fatalf("error %v wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if err := recoverWatchCommission(cfg, *w.binding, w.journal, "123"); err != nil {
				t.Fatal(err)
			}
			jobs, err := dispatch.ReadJournalStatus(*w.binding)
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 1 || jobs[0].Code != "operator_retry" {
				t.Fatalf("jobs %+v", jobs)
			}
			job, ok, err := w.journal.NextKind("commission_order")
			if err != nil || !ok || job.Kind != "commission_order" {
				t.Fatalf("claim %+v %v %v", job, ok, err)
			}
			if _, err := w.journal.RecoverCommission("123", 4); err == nil {
				t.Fatal("running intake bypassed by newer version")
			}
		})
	}
}
