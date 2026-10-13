package consumer

import (
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
