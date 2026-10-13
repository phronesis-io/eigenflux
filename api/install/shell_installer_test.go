package install

import (
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func TestShellInstallerRedirect(t *testing.T) {
	h := server.New()
	RegisterShellInstaller(h)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, path := range []string{"/install.sh", "/install.sh?ref=EF-1234abcd&url=https://untrusted.example"} {
			resp := ut.PerformRequest(h.Engine, method, path, nil).Result()
			if resp.StatusCode() != http.StatusTemporaryRedirect {
				t.Fatalf("%s %s: status %d", method, path, resp.StatusCode())
			}
			if got := string(resp.Header.Peek("Location")); got != shellInstallerURL {
				t.Fatalf("unexpected redirect %q", got)
			}
			if got := string(resp.Header.Peek("Cache-Control")); got != "no-store" {
				t.Fatalf("unexpected cache policy %q", got)
			}
		}
	}
}
