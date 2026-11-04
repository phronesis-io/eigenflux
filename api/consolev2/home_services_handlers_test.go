package consolev2

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"eigenflux_server/pkg/featureindex"
	"github.com/cloudwego/hertz/pkg/app/server"
)

type homeServicesSource struct {
	snapshots      []featureindex.CommissionCatalogueSnapshot
	statistics     []featureindex.CommissionStatisticsSnapshot
	current        map[int64]featureindex.CommissionCatalogueSnapshot
	err            error
	requestedLimit int
}

func (s *homeServicesSource) ListActiveIndexSnapshots(_ context.Context, _ int64, limit int) ([]featureindex.CommissionCatalogueSnapshot, int64, error) {
	s.requestedLimit = limit
	return s.snapshots, 0, s.err
}
func (s *homeServicesSource) BatchGetStatistics(context.Context, []int64) ([]featureindex.CommissionStatisticsSnapshot, error) {
	return s.statistics, s.err
}
func (s *homeServicesSource) GetStatistics(context.Context, int64) (featureindex.CommissionStatisticsSnapshot, error) {
	panic("unused")
}
func (s *homeServicesSource) GetIndexSnapshot(_ context.Context, id int64) (featureindex.CommissionCatalogueSnapshot, error) {
	return s.current[id], s.err
}
func homeServicesFixture() *homeServicesSource {
	s := &homeServicesSource{current: map[int64]featureindex.CommissionCatalogueSnapshot{}}
	for i := int64(1); i <= 9; i++ {
		item := featureindex.CommissionCatalogueSnapshot{CommissionID: i, SellerAgentID: 21, Status: "active", Title: "Published service", CatalogueVersion: 1}
		s.snapshots = append(s.snapshots, item)
		s.current[i] = item
		s.statistics = append(s.statistics, featureindex.CommissionStatisticsSnapshot{CommissionID: i, SellerAgentID: 21, CompletedCount: i / 2})
	}
	return s
}
func TestHomeServicesRankAndFreshPublication(t *testing.T) {
	source := homeServicesFixture()
	ranked, err := rankHomeServices(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if source.requestedLimit != 100 || len(ranked) != 9 || ranked[0].Snapshot.CommissionID != 9 || ranked[1].Snapshot.CommissionID != 8 {
		t.Fatalf("unexpected rank: %#v", ranked)
	}
	// A previously ranked service goes offline; another publishes a new revision.
	offline := source.current[9]
	offline.Status = "offline"
	source.current[9] = offline
	revised := source.current[8]
	revised.Title = "Current public revision"
	revised.CatalogueVersion = 2
	source.current[8] = revised
	result, err := currentHomeServices(context.Background(), source, ranked)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 6 || result[0].Snapshot.CommissionID != 8 || result[0].Snapshot.Title != "Current public revision" || result[5].Snapshot.CommissionID != 3 {
		t.Fatalf("unexpected current catalogue: %#v", result)
	}
}
func TestHomeServicesRejectUnavailableAndIncompleteSource(t *testing.T) {
	if _, err := rankHomeServices(context.Background(), nil); err == nil {
		t.Fatal("nil source succeeded")
	}
	source := homeServicesFixture()
	source.err = errors.New("transport")
	if _, err := rankHomeServices(context.Background(), source); err == nil {
		t.Fatal("failed source succeeded")
	}
	source.err = nil
	source.statistics = source.statistics[:1]
	if _, err := rankHomeServices(context.Background(), source); err == nil {
		t.Fatal("missing statistics succeeded")
	}
	source = homeServicesFixture()
	source.snapshots[0].Status = "draft"
	if _, err := rankHomeServices(context.Background(), source); err == nil {
		t.Fatal("unpublished source succeeded")
	}
	source = homeServicesFixture()
	source.current[9] = featureindex.CommissionCatalogueSnapshot{CommissionID: 9, SellerAgentID: 99, Status: "active", Title: "wrong seller"}
	if _, err := currentHomeServices(context.Background(), source, []rankedHomeService{{Snapshot: source.snapshots[8]}}); err == nil {
		t.Fatal("mismatched identity succeeded")
	}
}
func TestHomeServicesRequiresConsoleSession(t *testing.T) {
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	svc := &Service{}
	svc.RegisterHomeServices(h, nil, nil)
	status, _, _ := performJSON(t, h, http.MethodGet, "/api/v2/console/home/services", map[string]interface{}{})
	if status != http.StatusUnauthorized {
		t.Fatalf("status %d", status)
	}
}
