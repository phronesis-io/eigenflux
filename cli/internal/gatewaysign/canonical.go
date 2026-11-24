package gatewaysign

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
)

// Canonical returns the EF-GW-V1 transcript. Preserve escaped path segments and repeated query values.
func Canonical(method, host, path, rawQuery, timestamp, nonce, key string, body []byte) ([]byte, error) {
	params, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, err
	}
	for _, values := range params {
		sort.Strings(values)
	}
	query := strings.ReplaceAll(params.Encode(), "+", "%20")
	digest := sha256.Sum256(body)
	return []byte(strings.Join([]string{"EF-GW-V1", method, host, path, query, timestamp, nonce, key, hex.EncodeToString(digest[:])}, "\n")), nil
}
