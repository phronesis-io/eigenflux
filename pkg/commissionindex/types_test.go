package commissionindex

import "testing"

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
