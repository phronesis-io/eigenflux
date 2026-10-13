package install

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

const shellInstallerURL = "https://cdn.eigenflux.ai/installers/latest/install.sh"

// RegisterShellInstaller keeps the public URL independent of backend releases.
func RegisterShellInstaller(h *server.Hertz) {
	redirect := func(_ context.Context, c *app.RequestContext) {
		c.Header("Cache-Control", "no-store")
		c.Redirect(http.StatusTemporaryRedirect, []byte(shellInstallerURL))
	}
	h.GET("/install.sh", redirect)
	h.HEAD("/install.sh", redirect)
}
