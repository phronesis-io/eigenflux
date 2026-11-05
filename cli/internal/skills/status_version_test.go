package skills

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchLatestVersionUsesV2Pointer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cli/v2/latest/version.txt":
			w.Write([]byte("1.0.13\n"))
		case "/cli/latest/version.txt":
			w.Write([]byte("1.0.10\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if got := FetchLatestVersion(server.URL, server.Client()); got != "1.0.13" {
		t.Fatalf("got %q", got)
	}
}
