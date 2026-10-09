package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cli.eigenflux.ai/internal/dispatch"
)

func TestCommissionDeliveryUncertainUploadRetainsEvidence(t *testing.T) {
	order := paidCommissionTestOrder()
	var uploads int
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/orders/901":
			materialTestResponse(out, map[string]any{"order": order})
		case "/api/v1/orders/901/uploads":
			uploads++
			keys = append(keys, r.Header.Get(idempotencyHeader))
			http.Error(out, "uncertain", http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected write after uncertainty: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	job := claimPaidCommission(t, w)
	dir := t.TempDir()
	if err := w.journal.SetCommissionFulfillmentDirectory(job.ID, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	decision := dispatch.CommissionFulfillmentDecision{Version: 1, RequestID: job.ID, OrderID: "901", OrderVersion: 3, Outcome: "artifacts_ready", Summary: "Generated report", SelfCheck: "Read bytes", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "outputs/report.txt", RelativePath: "report.txt"}}}
	artifacts, err := validateCommissionArtifacts(dir, decision)
	if err != nil {
		t.Fatal(err)
	}
	result := dispatch.CommissionFulfillmentResult{Decision: decision, OutputDirectory: dir, Artifacts: artifacts}
	if err := w.deliverCommissionArtifacts(context.Background(), job, order, result, "session"); err != nil {
		t.Fatal(err)
	}
	got, err := dispatch.ReadJournalStatus(*w.binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range got {
		if record.ID == job.ID && (record.Status != "unknown" || record.CommissionFulfillment == nil) {
			t.Fatalf("uncertain upload lost: %+v", record)
		}
	}
	if uploads != 1 {
		t.Fatalf("unexpected retry count %d", uploads)
	}
	if err := w.journal.Retry(job.ID); err == nil {
		t.Fatal("uncertain upload can retry without reconciliation")
	}
	if err := w.journal.Reconcile(job.ID, "failed", ""); err != nil {
		t.Fatal(err)
	}
	if err := w.journal.Retry(job.ID); err != nil {
		t.Fatal(err)
	}
	job, ok, err := w.journal.NextKind("commission_fulfillment")
	if err != nil || !ok {
		t.Fatal("explicit retry not queued", err)
	}
	newDir := t.TempDir()
	if err := w.journal.SetCommissionFulfillmentDirectory(job.ID, newDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "report.txt"), []byte("regenerated report"), 0600); err != nil {
		t.Fatal(err)
	}
	result.OutputDirectory = newDir
	result.Artifacts, err = validateCommissionArtifacts(newDir, decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.deliverCommissionArtifacts(context.Background(), job, order, result, "retry-session"); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("regenerated output reused expired upload grant: %v", keys)
	}
}

func TestFreezeCommissionArtifactsRejectsChangedBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("original"))
	result := dispatch.CommissionFulfillmentResult{OutputDirectory: dir, Artifacts: []dispatch.CommissionVerifiedArtifact{{LogicalPath: "outputs/report.txt", RelativePath: "report.txt", ByteSize: 8, SHA256: hex.EncodeToString(digest[:])}}}
	_, cleanup, err := freezeCommissionArtifacts(result)
	cleanup()
	if err == nil {
		t.Fatal("changed bytes admitted for upload")
	}
}
