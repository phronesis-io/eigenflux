package selfupdate

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFailureKindKeepsOnlyBoundedCategories(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&downloadHTTPError{404}, "http_404"},
		{&downloadHTTPError{503}, "http_503"},
		{&url.Error{Op: "Get", URL: "https://private.invalid/token", Err: context.DeadlineExceeded}, "timeout"},
		{context.Canceled, "cancelled"},
		{&net.DNSError{Name: "private.invalid", Err: "not found"}, "dns_error"},
		{tls.RecordHeaderError{Msg: "private TLS detail"}, "tls_error"},
		{&os.PathError{Op: "rename", Path: "/private/agent/token", Err: os.ErrPermission}, "permission_denied"},
		{syscall.ENOSPC, "disk_full"},
		{&os.PathError{Op: "write", Path: "/private/file", Err: syscall.EIO}, fmt.Sprintf("os_%s_%d", runtime.GOOS, syscall.EIO)},
		{&os.PathError{Op: "remove", Path: "/private/backup", Err: syscall.ENOTEMPTY}, "directory_not_empty"},
		{syscall.EBUSY, "file_in_use"},
		{syscall.EROFS, "read_only"},
		{syscall.EXDEV, "cross_device"},
		{syscall.ECONNREFUSED, "connection_refused"},
		{io.ErrUnexpectedEOF, "transfer_incomplete"},
		{errors.New("unknown private detail"), "failed"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			if got := FailureKind(fmt.Errorf("retain original detail: %w", tc.err)); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
	if runtime.GOOS == "windows" {
		if got := FailureKind(syscall.Errno(32)); got != "file_in_use" {
			t.Fatalf("sharing violation: %s", got)
		}
		if got := FailureKind(syscall.Errno(112)); got != "disk_full" {
			t.Fatalf("disk full: %s", got)
		}
	}
}

func TestInstallationStepPreservesOriginalError(t *testing.T) {
	err := &os.LinkError{Op: "link", Old: "/private/old", New: "/private/backup", Err: os.ErrPermission}
	wrapped := atStep("backup_create", fmt.Errorf("retain previous CLI: %w", err))
	if !errors.Is(wrapped, os.ErrPermission) || !strings.Contains(wrapped.Error(), "/private/backup") {
		t.Fatal("local diagnostic or error identity was lost")
	}
	if got := failureCode("install", wrapped); got != "install_backup_create_permission_denied" {
		t.Fatal(got)
	}
}

type diagnosticTransport struct{ mode string }

func (d diagnosticTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "/latest/") && d.mode != "manifest_404" {
		return http.DefaultTransport.RoundTrip(req)
	}
	if d.mode == "timeout" {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("private error body")), Header: http.Header{}, Request: req}, nil
}

func TestUpdateFailureCodesReachResultAndRetainOldBinary(t *testing.T) {
	for _, tc := range []struct{ name, phase, code string }{
		{"manifest_404", "check", "check_http_404"},
		{"artifact_404", "download", "download_http_404"},
		{"timeout", "download", "download_timeout"},
		{"backup_remove", "install", "install_backup_remove_directory_not_empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, _, _ := fixture(t)
			if tc.name == "backup_remove" {
				o.OnProgress = func(p Progress) {
					if p.Phase == "install" {
						if err := os.Mkdir(o.Executable+".previous", 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(o.Executable+".previous", "retained"), []byte("x"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			} else {
				o.Client = &http.Client{Timeout: 100 * time.Millisecond, Transport: diagnosticTransport{tc.name}}
			}
			r := Check(context.Background(), o)
			if r.Status != "failed" || r.Phase != tc.phase || r.Reason != tc.code || r.Error == "" {
				t.Fatalf("missing diagnostic: %+v", r)
			}
			old, err := os.ReadFile(o.Executable)
			if err != nil || string(old) != string(script(o.Version)) {
				t.Fatalf("failed update changed installed binary: %v", err)
			}
			o.Now = o.Now.Add(time.Minute)
			if next := Check(context.Background(), o); next.Status != "deferred" || next.Reason != "retry_interval" {
				t.Fatalf("diagnostic changed retry behavior: %+v", next)
			}
		})
	}
}

func TestCompletePackageDownloadFailureHasHTTPStatus(t *testing.T) {
	root, pub, key := installationFixture(t)
	name := "eigenflux-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	m := Manifest{Version: "1.0.2", Artifacts: map[string]Artifact{name: testArtifact([]byte("CLI"))}, Packages: map[string]Artifact{name + ".zip": testArtifact([]byte("ZIP"))}}
	if err := Sign(&m, key); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cli/latest/package-release.json" {
			_ = json.NewEncoder(w).Encode(m)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	r := Check(context.Background(), Options{Home: t.TempDir(), Executable: root, Version: "1.0.1", Key: pub, CDN: api.URL, CompletePackage: true})
	if r.Status != "failed" || r.Reason != "download_http_404" || r.TargetVersion != m.Version {
		t.Fatalf("complete package failure: %+v", r)
	}
	if _, err := os.Stat(root + ".current.json"); !os.IsNotExist(err) {
		t.Fatal("failed download activated package")
	}
}
