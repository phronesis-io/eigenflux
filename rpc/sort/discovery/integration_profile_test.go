package discovery

import (
	"context"
	"reflect"
	"testing"

	"eigenflux_server/pkg/cache"
	embcodec "eigenflux_server/pkg/embedding"
	"eigenflux_server/pkg/integrationprofile"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestManagedProfileVectorBypassesLegacyCache(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := db.DB()
	conn.SetMaxOpenConns(1)
	defer conn.Close()
	if err := db.AutoMigrate(&integrationprofile.MatchingProfile{}); err != nil {
		t.Fatal(err)
	}
	want := []float32{0, 1}
	if err := db.Create(&integrationprofile.MatchingProfile{AgentID: 1, Keywords: `["managed-private"]`, Embedding: embcodec.Encode(want)}).Error; err != nil {
		t.Fatal(err)
	}
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	defer client.Close()
	ctx := context.Background()
	legacy := []float32{1, 0}
	if err := cache.NewEmbeddingCache(client, 0).Set(ctx, 1, embcodec.Encode(legacy)); err != nil {
		t.Fatal(err)
	}
	s := &Source{DB: db, Redis: client, ManagedIntegrations: true}
	got, err := s.ProfileVector(ctx, 1)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	s.ManagedIntegrations = false
	got, err = s.ProfileVector(ctx, 1)
	if err != nil || !reflect.DeepEqual(got, legacy) {
		t.Fatal("legacy path changed", got, err)
	}
}
