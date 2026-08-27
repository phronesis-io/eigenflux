package tradebff

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"testing"
	"time"
)

func TestDelegationGoldenVector(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index)
	}
	delegator, err := NewDelegator("test-2026", base64.RawURLEncoding.EncodeToString(seed))
	if err != nil {
		t.Fatal(err)
	}
	delegator.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	delegator.random = bytes.NewReader([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15})
	token, err := delegator.Token(DelegationRequest{AgentID: 42, Scope: "wallet:read", Method: http.MethodGet, Operation: "wallet.balance.read"})
	if err != nil {
		t.Fatal(err)
	}
	const expected = "efd1_test-2026.eyJ2ZXIiOjEsImlzcyI6ImVpZ2VuZmx1eC1hcGkiLCJhdWQiOiJlaWdlbmZsdXgtY29tbWlzc2lvbiIsInN1YiI6IjQyIiwic2NvcGUiOiJ3YWxsZXQ6cmVhZCIsImlhdCI6MTgwMDAwMDAwMCwiZXhwIjoxODAwMDAwMDMwLCJqdGkiOiIwMDAxMDIwMzA0MDUwNjA3MDgwOTBhMGIwYzBkMGUwZiIsIm1ldGhvZCI6IkdFVCIsIm9wZXJhdGlvbiI6IndhbGxldC5iYWxhbmNlLnJlYWQifQ.u6kMcXsVSO_SlRUOitPDYgsot5_tkLB0uWNuQcE-I8zCb2WIhHXA1y-Gm0NNUjBKsap-YO5XLHR6zyuf3yf7AA"
	if token != expected {
		t.Fatalf("token contract drifted:\n%s", token)
	}
}
