package replay_test

import (
	"strings"
	"testing"

	"eigenflux_server/tests/testutil"
)

func TestOfflineRecommendationIndexes(t *testing.T) {
	tests := []struct {
		name          string
		indexName     string
		wantColumns   string
		wantPredicate string
	}{
		{
			name:        "replay extraction and retention scan",
			indexName:   "idx_replay_logs_served_at",
			wantColumns: "served_at",
		},
		{
			name:          "hot recall surface window",
			indexName:     "idx_followup_labels_surface_reported_item",
			wantColumns:   "reported_at,item_id",
			wantPredicate: "surface",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var method, columns, predicate string
			var unique bool
			err := testutil.TestDB.QueryRow(`
				SELECT
					am.amname,
					i.indisunique,
					string_agg(a.attname, ',' ORDER BY key.ordinality),
					COALESCE(pg_get_expr(i.indpred, i.indrelid), '')
				FROM pg_index i
				JOIN pg_class idx ON idx.oid = i.indexrelid
				JOIN pg_am am ON am.oid = idx.relam
				JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS key(attnum, ordinality)
					ON key.ordinality <= i.indnkeyatts
				JOIN pg_attribute a
					ON a.attrelid = i.indrelid AND a.attnum = key.attnum
				WHERE idx.relname = $1
				GROUP BY am.amname, i.indisunique, i.indpred, i.indrelid
			`, tt.indexName).Scan(&method, &unique, &columns, &predicate)
			if err != nil {
				t.Fatalf("query index %s: %v", tt.indexName, err)
			}
			if method != "btree" {
				t.Fatalf("access method = %q, want btree", method)
			}
			if unique {
				t.Fatal("index must not be unique")
			}
			if columns != tt.wantColumns {
				t.Fatalf("columns = %q, want %q", columns, tt.wantColumns)
			}
			if tt.wantPredicate == "" {
				if predicate != "" {
					t.Fatalf("predicate = %q, want no predicate", predicate)
				}
				return
			}
			if !strings.Contains(predicate, "kind") || !strings.Contains(predicate, tt.wantPredicate) {
				t.Fatalf("predicate = %q, want kind = %q", predicate, tt.wantPredicate)
			}
		})
	}
}
