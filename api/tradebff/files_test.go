package tradebff

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/route/param"
)

type fileRoundTripper func(*http.Request) (*http.Response, error)

func (f fileRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fileContext(query string) *app.RequestContext {
	c := app.NewContext(2)
	c.Set("agent_id", int64(42))
	c.Params = param.Params{{Key: "order_id", Value: "123"}, {Key: "snapshot_id", Value: "456"}}
	c.Request.SetRequestURI("/file?path=outputs%2Fmingshu.md" + query)
	return c
}

func TestSnapshotFilePreservesIdentityVersionAndSafeContent(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(map[bool]string{true: "preview", false: "download"}[preview], func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/orders/123/snapshots/456/download" || r.URL.Query().Get("path") != "outputs/mingshu.md" {
					t.Fatal(r.URL)
				}
				claims := tokenClaims(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
				if claims.Subject != "42" || claims.Scope != "orders:files:read" || claims.Operation != "console.trade.orders.files.read" {
					t.Fatal("wrong delegation")
				}
				json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"grant": map[string]any{"method": "GET", "url": "https://bucket.oss-cn-hangzhou.aliyuncs.com/file?signature=secret", "expires_at": time.Now().Add(time.Minute).UnixMilli()}}})
			}))
			defer upstream.Close()
			s := configuredService(t, upstream.URL)
			s.client.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "https" {
					return http.DefaultTransport.RoundTrip(r)
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Fatal("leaked credentials")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("# full file\n<script>alert(1)</script>")), Header: http.Header{}}, nil
			})
			query := ""
			if preview {
				query = "&preview=1"
			}
			c := fileContext(query)
			s.TradeOrderFile(context.Background(), c)
			if c.Response.StatusCode() != 200 || !strings.Contains(string(c.Response.Body()), "# full file") {
				t.Fatalf("%d %s", c.Response.StatusCode(), c.Response.Body())
			}
			if string(c.Response.Header.Peek("Cache-Control")) != "private, no-store" {
				t.Fatal("cacheable private file")
			}
			if preview && !strings.HasPrefix(string(c.Response.Header.ContentType()), "text/plain") {
				t.Fatal("unsafe preview MIME")
			}
			if !preview && !strings.Contains(string(c.Response.Header.Peek("Content-Disposition")), "attachment") {
				t.Fatal("missing attachment")
			}
		})
	}
}

func TestSnapshotFileRejectsInvalidRequestsAndUntrustedGrants(t *testing.T) {
	for _, target := range []string{"http://bucket.aliyuncs.com/file", "https://127.0.0.1/file", "https://bucket.aliyuncs.com.evil.test/file", "https://user:pass@bucket.aliyuncs.com/file"} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"grant": map[string]any{"method": "GET", "url": target, "expires_at": time.Now().Add(time.Minute).UnixMilli()}}})
		}))
		c := fileContext("")
		configuredService(t, upstream.URL).TradeOrderFile(context.Background(), c)
		if c.Response.StatusCode() != 502 {
			t.Fatalf("accepted %s", target)
		}
		upstream.Close()
	}
	c := fileContext("")
	c.Params[1].Value = "0"
	NewUnavailable("").TradeOrderFile(context.Background(), c)
	if c.Response.StatusCode() != 400 {
		t.Fatal("invalid snapshot accepted")
	}
	c = fileContext("")
	c.Set("agent_id", int64(0))
	NewUnavailable("").TradeOrderFile(context.Background(), c)
	if c.Response.StatusCode() != 401 {
		t.Fatal("missing principal accepted")
	}
}

func TestSnapshotFilePreviewLimitAndAuthorization(t *testing.T) {
	for _, denied := range []bool{false, true} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if denied {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"code":403,"msg":"denied"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"grant": map[string]any{"method": "GET", "url": "https://bucket.oss-cn-hangzhou.aliyuncs.com/file", "expires_at": time.Now().Add(time.Minute).UnixMilli()}}})
		}))
		s := configuredService(t, upstream.URL)
		reads := 0
		s.client.http.Transport = fileRoundTripper(func(r *http.Request) (*http.Response, error) {
			if r.URL.Scheme != "https" {
				return http.DefaultTransport.RoundTrip(r)
			}
			reads++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("a", (1<<20)+1))), Header: http.Header{}}, nil
		})
		c := fileContext("&preview=1")
		s.TradeOrderFile(context.Background(), c)
		if denied {
			if c.Response.StatusCode() != 403 || reads != 0 {
				t.Fatal("authorization did not stop storage access")
			}
		} else if c.Response.StatusCode() != 413 || reads != 1 {
			t.Fatal("preview limit not enforced")
		}
		upstream.Close()
	}
}
