package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/commissionapi"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
)

func materialTestOrder() commissionIntakeOrder {
	return commissionIntakeOrder{OrderID: 901, BuyerID: 43, SellerID: 42, Version: 3, State: "pending_payment", SnapshotID: 999, Contract: json.RawMessage(`{"requires_materials":true}`)}
}

func materialTestEntry() map[string]any {
	sum := sha256.Sum256([]byte("input contents"))
	return map[string]any{"logical_path": "inputs/request.txt", "object_id": "9007199254740993", "snapshot_id": "9007199254740995", "byte_size": len("input contents"), "sha256": hex.EncodeToString(sum[:]), "uploader_agent_id": "43"}
}

func materialTestDetail(inputs []map[string]any) map[string]any {
	return map[string]any{"order_id": "901", "role": "seller", "version": 3, "state": "pending_payment", "counterparty": map[string]any{"agent_id": "43"}, "files": map[string]any{"input": inputs, "output": []any{}}}
}

func materialTestWatch(t *testing.T, endpoint string) *accountWatch {
	t.Helper()
	w := newCommissionWatch(t, endpoint, "commission_order")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err = cfg.UpdateServerWithCommission(w.server.Name, "", "", endpoint); err != nil {
		t.Fatal(err)
	}
	w.server.CommissionEndpoint = endpoint
	return w
}

func materialTestResponse(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func TestPrepareCommissionMaterialsUsesInputSnapshotsAndOmitsCredentials(t *testing.T) {
	var server *httptest.Server
	var grants, downloads atomic.Int32
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/console/trade/orders/901":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Error("manifest request has no pinned credentials")
			}
			materialTestResponse(w, materialTestDetail([]map[string]any{materialTestEntry()}))
		case "/api/v1/orders/901/snapshots/9007199254740995/download":
			grants.Add(1)
			if r.URL.Query().Get("path") != "inputs/request.txt" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Error("wrong input path/credentials")
			}
			materialTestResponse(w, map[string]any{"grant": commissionapi.TransferGrant{ObjectID: 9007199254740993, Method: "GET", URL: server.URL + "/blob?signature=private", Headers: map[string]string{"X-Signed": "grant-header"}}})
		case "/blob":
			downloads.Add(1)
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-CLI-Ver") != "" {
				t.Error("API credentials/metadata leaked to object store")
			}
			if r.Header.Get("X-Signed") != "grant-header" {
				t.Error("grant header missing")
			}
			_, _ = io.WriteString(w, "input contents")
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	watch := materialTestWatch(t, server.URL)
	directory := t.TempDir()
	files, err := watch.prepareCommissionMaterials(context.Background(), materialTestOrder(), directory)
	if err != nil || len(files) != 1 || grants.Load() != 1 || downloads.Load() != 1 {
		t.Fatalf("prepare: %+v %v", files, err)
	}
	file := files[0]
	if filepath.Dir(file.Path) != directory || filepath.Ext(file.Path) != ".txt" || file.LogicalPath != "inputs/request.txt" || file.ByteSize != 14 {
		t.Fatalf("unsafe or incorrect local path: %+v", file)
	}
	contents, err := os.ReadFile(file.Path)
	if err != nil || string(contents) != "input contents" {
		t.Fatalf("wrong local bytes: %q %v", contents, err)
	}
	info, err := os.Stat(file.Path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private file mode: %v %v", info, err)
	}
}

func TestPrepareCommissionMaterialsRejectsManifestBeforeDownload(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any, map[string]any)
		want   error
	}{
		{"order", func(d, f map[string]any) { d["order_id"] = "902" }, errCommissionMaterialsInvalid},
		{"buyer role", func(d, f map[string]any) { d["role"] = "buyer" }, errCommissionMaterialsInvalid},
		{"version", func(d, f map[string]any) { d["version"] = 4 }, errCommissionMaterialsInvalid},
		{"state", func(d, f map[string]any) { d["state"] = "in_progress" }, errCommissionMaterialsInvalid},
		{"counterparty", func(d, f map[string]any) { d["counterparty"] = map[string]any{"agent_id": "44"} }, errCommissionMaterialsInvalid},
		{"missing input field", func(d, f map[string]any) { d["files"] = map[string]any{} }, errCommissionMaterialsInvalid},
		{"null input field", func(d, f map[string]any) { d["files"] = map[string]any{"input": nil} }, errCommissionMaterialsInvalid},
		{"object", func(d, f map[string]any) { f["object_id"] = "0" }, errCommissionMaterialsInvalid},
		{"snapshot", func(d, f map[string]any) { f["snapshot_id"] = "0" }, errCommissionMaterialsInvalid},
		{"uploader", func(d, f map[string]any) { f["uploader_agent_id"] = "42" }, errCommissionMaterialsInvalid},
		{"digest", func(d, f map[string]any) { f["sha256"] = "bad" }, errCommissionMaterialsInvalid},
		{"size missing", func(d, f map[string]any) { delete(f, "byte_size") }, errCommissionMaterialsInvalid},
		{"negative size", func(d, f map[string]any) { f["byte_size"] = -1 }, errCommissionMaterialsInvalid},
		{"oversized", func(d, f map[string]any) { f["byte_size"] = commissionMaterialMaxBytes + 1 }, errCommissionMaterialsLimit},
		{"path traversal", func(d, f map[string]any) { f["logical_path"] = "../outside" }, errCommissionMaterialsInvalid},
		{"path absolute", func(d, f map[string]any) { f["logical_path"] = "/outside" }, errCommissionMaterialsInvalid},
		{"path control", func(d, f map[string]any) { f["logical_path"] = "inputs/\nfile" }, errCommissionMaterialsInvalid},
		{"duplicate", func(d, f map[string]any) { d["files"] = map[string]any{"input": []any{f, f}} }, errCommissionMaterialsInvalid},
		{"many files", func(d, f map[string]any) { d["files"] = map[string]any{"input": make([]any, 129)} }, errCommissionMaterialsLimit},
		{"total bytes", func(d, f map[string]any) {
			f["byte_size"] = commissionMaterialMaxBytes
			second := materialTestEntry()
			second["logical_path"] = "inputs/second.txt"
			d["files"] = map[string]any{"input": []any{f, second}}
		}, errCommissionMaterialsLimit},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var grantCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/console/trade/orders/901" {
					grantCalls.Add(1)
					http.NotFound(w, r)
					return
				}
				file := materialTestEntry()
				detail := materialTestDetail([]map[string]any{file})
				test.change(detail, file)
				materialTestResponse(w, detail)
			}))
			defer server.Close()
			watch := materialTestWatch(t, server.URL)
			files, err := watch.prepareCommissionMaterials(context.Background(), materialTestOrder(), t.TempDir())
			if !errors.Is(err, test.want) || files != nil || grantCalls.Load() != 0 {
				t.Fatalf("files=%v err=%v grants=%d", files, err, grantCalls.Load())
			}
		})
	}
	if !errors.Is(errCommissionMaterialsLimit, dispatch.ErrNeedsUser) {
		t.Fatal("limits must request user intervention")
	}
}

func TestPrepareCommissionMaterialsLeavesEmptyManifestForModelInspection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		materialTestResponse(w, materialTestDetail([]map[string]any{}))
	}))
	defer server.Close()
	watch := materialTestWatch(t, server.URL)
	files, err := watch.prepareCommissionMaterials(context.Background(), materialTestOrder(), t.TempDir())
	if err != nil || files == nil || len(files) != 0 {
		t.Fatalf("unexpected no-materials result: %v %v", files, err)
	}
}

func TestPrepareCommissionMaterialsValidatesGrantAndActualBytes(t *testing.T) {
	for _, failure := range []string{"object", "method", "userinfo", "scheme", "authorization", "expired", "redirect", "missing", "length", "digest", "chunked excess"} {
		t.Run(failure, func(t *testing.T) {
			var server *httptest.Server
			var unexpected atomic.Int32
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/console/trade/orders/901":
					materialTestResponse(w, materialTestDetail([]map[string]any{materialTestEntry()}))
				case "/api/v1/orders/901/snapshots/9007199254740995/download":
					grant := commissionapi.TransferGrant{ObjectID: 9007199254740993, Method: "GET", URL: server.URL + "/blob?signature=private"}
					switch failure {
					case "object":
						grant.ObjectID++
					case "method":
						grant.Method = "POST"
					case "userinfo":
						grant.URL = strings.Replace(grant.URL, "://", "://secret:private@", 1)
					case "scheme":
						grant.URL = "file:///private"
					case "authorization":
						grant.Headers = map[string]string{"Authorization": "Bearer private"}
					case "expired":
						grant.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
					}
					materialTestResponse(w, map[string]any{"grant": grant})
				case "/blob":
					switch failure {
					case "redirect":
						http.Redirect(w, r, server.URL+"/redirect?signature=private", http.StatusFound)
					case "missing":
						http.NotFound(w, r)
					case "length":
						_, _ = io.WriteString(w, "short")
					case "digest":
						_, _ = io.WriteString(w, "wrong contents")
					case "chunked excess":
						w.(http.Flusher).Flush()
						_, _ = io.WriteString(w, "input contents plus extra")
					default:
						unexpected.Add(1)
						_, _ = io.WriteString(w, "input contents")
					}
				default:
					unexpected.Add(1)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			watch := materialTestWatch(t, server.URL)
			directory := t.TempDir()
			files, err := watch.prepareCommissionMaterials(context.Background(), materialTestOrder(), directory)
			if err == nil || files != nil || unexpected.Load() != 0 {
				t.Fatalf("invalid material accepted: %v %v unexpected=%d", files, err, unexpected.Load())
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("secret URL leaked: %v", err)
			}
			entries, _ := os.ReadDir(directory)
			if len(entries) != 0 {
				t.Fatal("unverified partial download retained")
			}
			if failure == "missing" && !errors.Is(err, errCommissionMaterialsMissing) {
				t.Fatalf("missing input classification: %v", err)
			}
		})
	}
}

func TestPrepareCommissionMaterialsHonorsCancellation(t *testing.T) {
	started := make(chan struct{})
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/console/trade/orders/901":
			materialTestResponse(w, materialTestDetail([]map[string]any{materialTestEntry()}))
		case "/api/v1/orders/901/snapshots/9007199254740995/download":
			materialTestResponse(w, map[string]any{"grant": commissionapi.TransferGrant{ObjectID: 9007199254740993, Method: "GET", URL: server.URL + "/blob"}})
		case "/blob":
			w.Header().Set("Content-Length", strconv.Itoa(len("input contents")))
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	watch := materialTestWatch(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	directory := t.TempDir()
	done := make(chan error, 1)
	go func() { _, err := watch.prepareCommissionMaterials(ctx, materialTestOrder(), directory); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("download ignored cancellation")
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 0 {
		t.Fatal("cancelled partial download retained")
	}
}

func TestPrepareCommissionMaterialsAcceptsServiceGrantWithoutObjectID(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/console/trade/orders/901":
			materialTestResponse(w, materialTestDetail([]map[string]any{materialTestEntry()}))
		case "/api/v1/orders/901/snapshots/9007199254740995/download":
			// downloadToAPI at Commission 418ed218 does not set object_id.
			materialTestResponse(w, map[string]any{"grant": map[string]any{"method": "GET", "url": server.URL + "/blob"}})
		case "/blob":
			_, _ = io.WriteString(w, "input contents")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	watch := materialTestWatch(t, server.URL)
	files, err := watch.prepareCommissionMaterials(context.Background(), materialTestOrder(), t.TempDir())
	if err != nil || len(files) != 1 {
		t.Fatalf("service download grant rejected: %v %v", files, err)
	}
}

func TestPrepareCommissionMaterialsDistinguishesRouteAndFileMissing(t *testing.T) {
	for _, stage := range []string{"manifest", "grant"} {
		t.Run(stage, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stage == "grant" && r.URL.Path == "/api/v2/console/trade/orders/901" {
					materialTestResponse(w, materialTestDetail([]map[string]any{materialTestEntry()}))
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			watch := materialTestWatch(t, server.URL)
			_, err := watch.prepareCommissionMaterials(context.Background(), materialTestOrder(), t.TempDir())
			want := errCommissionMaterialsUnavailable
			if stage == "grant" {
				want = errCommissionMaterialsMissing
			}
			if !errors.Is(err, want) {
				t.Fatalf("404 classification: got %v want %v", err, want)
			}
		})
	}
}
