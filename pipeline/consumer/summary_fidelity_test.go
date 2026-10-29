package consumer

import (
	"strings"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"testing"
)

func TestPGCSummaryPublisherUsesAuthoritativeIdentity(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	defer sql.Close()
	require.NoError(t, db.Exec("CREATE TABLE agents(agent_id INTEGER PRIMARY KEY, email TEXT)").Error)
	require.NoError(t, db.Exec("INSERT INTO agents VALUES (1,'source@pgc.eigenflux.one'),(2,'person@example.com'),(3,'source@pgc.eigenflux.one.evil.test'),(4,'source@bot.eigenflux.one')").Error)
	suffixes := []string{"@pgc.eigenflux.one", "@bot.eigenflux.one"}
	for _, tc := range []struct {
		id   int64
		want bool
	}{{1, true}, {2, false}, {3, false}, {4, true}} {
		got, err := pgcSummaryPublisher(db, tc.id, suffixes)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
	_, err = pgcSummaryPublisher(db, 99, suffixes)
	require.Error(t, err)
	require.NoError(t, db.Exec("DROP TABLE agents").Error)
	_, err = pgcSummaryPublisher(db, 1, suffixes)
	require.Error(t, err)
}

func TestPublisherSummaryAcceptsOnlyUsableNotesSummary(t *testing.T) {
	long := strings.Repeat("字", maxPublisherSummaryRunes+1)
	exact := strings.Repeat("字", maxPublisherSummaryRunes)
	for _, tc := range []struct {
		notes string
		want  string
		ok    bool
	}{
		{`{"summary":"  KLM suspends Amsterdam-Dubai flights.  "}`, "KLM suspends Amsterdam-Dubai flights.", true},
		{`{"summary":"` + exact + `"}`, exact, true},
		{`{"summary":"` + long + `"}`, "", false},
		{`{"summary":""}`, "", false},
		{`{"summary":"   "}`, "", false},
		{`{"summary":null}`, "", false},
		{`{"summary":42}`, "", false},
		{`{"type":"info"}`, "", false},
		{`{"summary":"Source-linked report; see the full broadcast for details."}`, "", false},
		{`free text notes`, "", false},
		{``, "", false},
	} {
		got, ok := publisherSummary(tc.notes)
		require.Equal(t, tc.ok, ok, tc.notes)
		require.Equal(t, tc.want, got, tc.notes)
	}
}

func TestSummaryRouteOnlyTrustsPGCPublisherSummary(t *testing.T) {
	notes := `{"summary":"KLM suspends Amsterdam-Dubai flights."}`
	for _, tc := range []struct {
		name              string
		fidelity, pubSumm bool
		pgc               bool
		notes             string
		want              summaryRoute
		wantSummary       string
	}{
		{"ugc never trusts notes", true, true, false, notes, summaryRouteServer, ""},
		{"pgc publisher summary", true, true, true, notes, summaryRoutePublisher, "KLM suspends Amsterdam-Dubai flights."},
		{"pgc without usable summary keeps audit", true, true, true, `{"summary":""}`, summaryRouteAudited, ""},
		{"switch off keeps audit", true, false, true, notes, summaryRouteAudited, ""},
		{"both off keeps server", false, false, true, notes, summaryRouteServer, ""},
		{"publisher only, no audit fallback", false, true, true, `{}`, summaryRouteServer, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &ItemConsumer{summaryFidelity: tc.fidelity, pgcPublisherSummary: tc.pubSumm}
			got, summary := c.summaryRoute(tc.pgc, tc.notes)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.wantSummary, summary)
		})
	}
}
