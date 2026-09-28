// Package featureconfig bundles the default feature view configuration.
// Services load external YAML at startup and hot-reload subsequent changes.
package featureconfig

import "embed"

//go:embed *.yaml
var Defaults embed.FS
