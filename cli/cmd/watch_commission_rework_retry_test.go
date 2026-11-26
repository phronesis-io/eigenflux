package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"cli.eigenflux.ai/internal/dispatch"
)

type reworkStagedObject struct {
	id              int64
	key, path, hash string
	size            int64
	bytes           []byte
	admitted        bool
	puts            int
}

type reworkRetryFixture struct {
	mu                                            sync.Mutex
	order                                         commissionIntakeOrder
	old, sealed                                   map[string][]byte
	objects                                       map[string]*reworkStagedObject
	requests                                      map[string]string
	faulted, delivered                            bool
	deliverCalls, formalSubmissions, oldDownloads int
}

func newReworkRetryFixture(t *testing.T, mode string) (*reworkRetryFixture, *httptest.Server) {
	t.Helper()
	f := &reworkRetryFixture{order: testReworkOrder(), old: map[string][]byte{"outputs/report.txt": []byte("first sealed report"), "outputs/appendix.txt": []byte("first sealed appendix")}, objects: map[string]*reworkStagedObject{}, requests: map[string]string{}}
	f.order.Contract = json.RawMessage(`{"fulfillment_skill":"input-review","requires_materials":false,"delivery_spec_text":"Deliver UTF-8 reports at outputs/report.txt and outputs/appendix.txt.","delivery_spec_schema":"{}"}`)
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		oldID := strconv.FormatInt(int64(f.order.SnapshotID), 10)
		newID := strconv.FormatInt(int64(f.order.SnapshotID)+1, 10)
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			key := r.Header.Get(idempotencyHeader)
			if key == "" {
				t.Error("missing idempotency key")
			}
			bound := r.URL.Path + "\n" + string(raw)
			if prior, ok := f.requests[key]; ok && prior != bound {
				t.Error("retry changed its original key/body binding")
			}
			f.requests[key] = bound
		}
		fail := func(phase string) bool {
			if !f.faulted && mode == phase {
				f.faulted = true
				http.Error(out, "temporary unavailable", 503)
				return true
			}
			return false
		}
		switch r.URL.Path {
		case "/api/v1/orders/901":
			order := f.order
			if f.delivered {
				order.State = "validating"
				order.Version++
				order.SnapshotID++
				review := *order.DeliveryReview
				review.SubmissionNumber = 2
				review.Status = "reviewing"
				review.SnapshotID = json.Number(newID)
				review.ReworkDueAt = 0
				order.DeliveryReview = &review
			}
			materialTestResponse(out, map[string]any{"order": order})
		case "/api/v2/console/trade/orders/901":
			materialTestResponse(out, map[string]any{"order_id": "901", "role": "seller", "version": f.order.Version, "state": f.order.State, "counterparty": map[string]string{"agent_id": "41"}, "files": map[string]any{"input": []any{}}})
		case "/api/v1/orders/901/uploads":
			var body struct {
				Path string `json:"logical_path"`
				Size int64  `json:"byte_size"`
				Hash string `json:"sha256"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || f.old[body.Path] == nil {
				t.Error("invalid upload manifest")
				http.Error(out, "invalid", 400)
				return
			}
			object := f.objects[body.Path]
			if object == nil {
				object = &reworkStagedObject{id: int64(903 + len(f.objects)), key: r.Header.Get(idempotencyHeader), path: body.Path, size: body.Size, hash: body.Hash}
				f.objects[body.Path] = object
			} else if object.key != r.Header.Get(idempotencyHeader) || object.size != body.Size || object.hash != body.Hash {
				t.Error("allocated a new object or changed pinned metadata")
				http.Error(out, "conflict", 409)
				return
			}
			if fail("lost_begin") || mode == "partial_upload" && body.Path == "outputs/appendix.txt" && fail("partial_upload") {
				return
			}
			grant := map[string]any{"object_id": object.id}
			if !object.admitted {
				grant["method"] = "PUT"
				grant["url"] = "http://" + r.Host + "/storage/" + strconv.FormatInt(object.id, 10)
			}
			materialTestResponse(out, map[string]any{"grant": grant})
		case "/api/v1/orders/901/uploads/confirm":
			var body struct {
				ID int64 `json:"object_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			var object *reworkStagedObject
			for _, candidate := range f.objects {
				if candidate.id == body.ID {
					object = candidate
				}
			}
			if object == nil {
				t.Error("confirm changed object ownership")
				http.Error(out, "forbidden", 403)
				return
			}
			digest := sha256.Sum256(object.bytes)
			if int64(len(object.bytes)) != object.size || hex.EncodeToString(digest[:]) != object.hash {
				t.Error("server admitted unverified bytes")
				http.Error(out, "mismatch", 400)
				return
			}
			object.admitted = true
			if fail("lost_confirm") {
				return
			}
			materialTestResponse(out, map[string]any{})
		case "/api/v1/orders/901/deliver":
			f.deliverCalls++
			var body struct {
				Version int64          `json:"expected_version"`
				Files   []materialFile `json:"output_files"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Version != 5 || len(body.Files) != 2 {
				t.Error("wrong rework delivery fence")
				http.Error(out, "invalid", 400)
				return
			}
			for _, file := range body.Files {
				object := f.objects[file.LogicalPath]
				if object == nil || !object.admitted || object.hash != file.SHA256 || object.size != file.ByteSize {
					t.Error("deliver skipped admission/manifest verification")
					http.Error(out, "mismatch", 400)
					return
				}
			}
			if fail("deliver_unavailable") {
				return
			}
			if f.delivered {
				t.Error("duplicate formal submission")
				http.Error(out, "conflict", 409)
				return
			}
			f.sealed = map[string][]byte{}
			for path, object := range f.objects {
				f.sealed[path] = append([]byte{}, object.bytes...)
			}
			if mode == "new_snapshot_mismatch" {
				f.sealed["outputs/report.txt"] = []byte("unrelated object bytes")
			}
			f.delivered = true
			f.formalSubmissions++
			if fail("lost_deliver") {
				return
			}
			materialTestResponse(out, map[string]any{})
		case "/download-sealed":
			if r.Header.Get("Authorization") != "" {
				t.Error("credentials leaked to sealed storage")
			}
			body := f.old[r.URL.Query().Get("path")]
			if r.URL.Query().Get("snapshot") == newID {
				body = f.sealed[r.URL.Query().Get("path")]
			} else {
				f.oldDownloads++
			}
			if body == nil {
				http.NotFound(out, r)
				return
			}
			_, _ = out.Write(body)
		default:
			if strings.HasPrefix(r.URL.Path, "/storage/") {
				if r.Header.Get("Authorization") != "" {
					t.Error("credentials leaked to upload storage")
				}
				id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/storage/"), 10, 64)
				var object *reworkStagedObject
				for _, candidate := range f.objects {
					if candidate.id == id {
						object = candidate
					}
				}
				if object == nil {
					http.NotFound(out, r)
					return
				}
				body, _ := io.ReadAll(r.Body)
				digest := sha256.Sum256(body)
				if object.hash != hex.EncodeToString(digest[:]) || object.size != int64(len(body)) {
					t.Error("upload changed frozen bytes")
				}
				object.bytes = append([]byte{}, body...)
				object.puts++
				if fail("temporary_storage") {
					return
				}
				out.WriteHeader(200)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/v1/orders/901/snapshots/") && strings.HasSuffix(r.URL.Path, "/download") {
				snapshot := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/orders/901/snapshots/"), "/download")
				if snapshot != oldID && (snapshot != newID || !f.delivered) {
					http.NotFound(out, r)
					return
				}
				values := url.Values{"snapshot": {snapshot}, "path": {r.URL.Query().Get("path")}}
				materialTestResponse(out, map[string]any{"grant": map[string]any{"method": "GET", "url": "http://" + r.Host + "/download-sealed?" + values.Encode()}})
				return
			}
			t.Errorf("unexpected rework request %s %s", r.Method, r.URL.Path)
			http.NotFound(out, r)
		}
	}))
	return f, server
}

func claimReworkWithContract(t *testing.T, w *accountWatch, order commissionIntakeOrder) dispatch.Job {
	t.Helper()
	intake := claimCommissionIntakeVersion(t, w, 1)
	decision := dispatch.CommissionIntakeDecision{Version: 1, RequestID: intake.ID, OrderID: "901", OrderVersion: 1, Outcome: "ready", Summary: "Original inputs inspected", InspectedFiles: []string{}}
	fingerprint, err := commissionInputFingerprint(order, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.journal.SaveCommissionReadiness(intake.ID, dispatch.CommissionReadiness{Result: decision, InputFingerprint: fingerprint}); err != nil {
		t.Fatal(err)
	}
	if err := w.journal.CompleteCommissionIntake(intake.ID, decision, "checked"); err != nil {
		t.Fatal(err)
	}
	paid := claimCommissionIntakeVersion(t, w, 3)
	if err := w.journal.QueueCommissionFromReadiness(paid.ID, intake.ID, "901", 3); err != nil {
		t.Fatal(err)
	}
	first, _, err := w.journal.NextKind("commission_fulfillment")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.journal.Update(first.ID, "completed", "commission_delivery_confirmed", "first-session", ""); err != nil {
		t.Fatal(err)
	}
	rework := claimCommissionIntakeVersion(t, w, 5)
	if err := w.dispatchCommissionIntake(context.Background(), rework); err != nil {
		t.Fatal(err)
	}
	job, ok, err := w.journal.NextKind("commission_fulfillment")
	if err != nil || !ok {
		t.Fatal("missing rework", err)
	}
	return job
}

func TestCommissionReworkRetriesStagingWithoutReadingPreviousSealedBytes(t *testing.T) {
	for _, mode := range []string{"lost_begin", "temporary_storage", "lost_confirm", "partial_upload", "deliver_unavailable", "lost_deliver", "new_snapshot_mismatch"} {
		t.Run(mode, func(t *testing.T) {
			f, server := newReworkRetryFixture(t, mode)
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			job := claimReworkWithContract(t, w, f.order)
			calls := 0
			w.runAgent = func(_ context.Context, request dispatch.Request) (dispatch.Result, error) {
				calls++
				data := fulfillmentTestPayload(t, request)
				artifacts := []dispatch.CommissionArtifact{}
				for _, name := range []string{"report.txt", "appendix.txt"} {
					if err := os.WriteFile(filepath.Join(data.OutputDirectory, name), []byte("revised contractual "+name), 0600); err != nil {
						t.Fatal(err)
					}
					artifacts = append(artifacts, dispatch.CommissionArtifact{LogicalPath: "outputs/" + name, RelativePath: name})
				}
				result := dispatch.CommissionFulfillmentDecision{Version: 1, RequestID: request.ID, OrderID: "901", OrderVersion: 5, Outcome: "artifacts_ready", Summary: "Regenerated both contractual files", SelfCheck: "Inspected revised report and appendix bytes", Artifacts: artifacts}
				raw, _ := json.Marshal(result)
				return dispatch.Result{Text: string(raw), SessionID: "rework-once"}, nil
			}
			if err := w.dispatchCommissionFulfillment(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if mode != "new_snapshot_mismatch" {
				pending := w.commissionDeliveryJob(job)
				if pending.Status != "pending" || pending.CommissionDelivery == nil {
					t.Fatalf("technical failure not scheduled: %+v", pending)
				}
				retry := dueCommissionDelivery(t, w, job.ID)
				if err := w.dispatchCommissionFulfillment(context.Background(), retry); err != nil {
					t.Fatal(err)
				}
			}
			final := w.commissionDeliveryJob(job)
			if mode == "new_snapshot_mismatch" {
				if final.Status != "unknown" || final.Code == "commission_delivery_confirmed" {
					t.Fatalf("unverified new snapshot confirmed: %+v", final)
				}
			} else if final.Status != "completed" || final.Code != "commission_delivery_confirmed" || final.CommissionDelivery.Attempts != 2 {
				t.Fatalf("rework recovery failed: %+v", final)
			}
			if calls != 1 || f.formalSubmissions != 1 || f.oldDownloads != 0 {
				t.Fatalf("model/submission/previous-snapshot counts=%d/%d/%d", calls, f.formalSubmissions, f.oldDownloads)
			}
			if string(f.old["outputs/report.txt"]) != "first sealed report" || string(f.old["outputs/appendix.txt"]) != "first sealed appendix" {
				t.Fatal("previous sealed bytes changed")
			}
			if len(f.objects) != 2 {
				t.Fatal("retries allocated replacement objects")
			}
			if mode == "partial_upload" && f.objects["outputs/report.txt"].puts != 1 {
				t.Fatal("already admitted first artifact uploaded again")
			}
			if mode == "lost_deliver" && f.deliverCalls != 1 {
				t.Fatal("uncertain committed delivery was resubmitted")
			}
			for _, object := range f.objects {
				if strings.Contains(object.key, "first-session") || bytes.Equal(object.bytes, f.old[object.path]) {
					t.Fatal("first submission artifacts or identity reused")
				}
			}
		})
	}
}
