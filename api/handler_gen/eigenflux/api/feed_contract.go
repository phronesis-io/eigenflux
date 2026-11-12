package api

import "eigenflux_server/pkg/feedcontract"

// feedContractPath is resolved relative to the process working directory, the
// same convention used for the other static assets served by this gateway
// (see main.go's static/BOOTSTRAP.md etc.).
const feedContractPath = feedcontract.DefaultPath

// loadFeedContract reads and trims the contract file, returning "" (and logging
// a warning) when it cannot be read so the caller can omit the field.
func loadFeedContract(path string) string {
	return feedcontract.Load(path)
}
