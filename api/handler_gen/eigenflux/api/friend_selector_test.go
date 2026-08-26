package api

import (
	"context"
	"testing"

	apimodel "eigenflux_server/api/model/eigenflux/api"
)

func TestResolveToUIDPreservesLegacyUIDPriority(t *testing.T) {
	shortID, uid := "AbCdE", "123"
	got, code, message := resolveToUID(context.Background(), &apimodel.SendFriendRequestReq{
		ToShortID: &shortID,
		ToUID:     &uid,
	})
	if got != 123 || code != 0 || message != "" {
		t.Fatalf("got=(%d,%d,%q), want (123,0,empty)", got, code, message)
	}
}

func TestResolveToUIDRejectsMalformedShortIDWithoutDatabaseLookup(t *testing.T) {
	shortID := "abc1e"
	_, code, _ := resolveToUID(context.Background(), &apimodel.SendFriendRequestReq{ToShortID: &shortID})
	if code != 404 {
		t.Fatalf("code=%d, want 404", code)
	}
}

func TestResolveToUIDKeepsLegacyNumericSelectorDuringRollout(t *testing.T) {
	uid := "123"
	got, code, message := resolveToUID(context.Background(), &apimodel.SendFriendRequestReq{ToUID: &uid})
	if got != 123 || code != 0 || message != "" {
		t.Fatalf("got=(%d,%d,%q), want (123,0,empty)", got, code, message)
	}
}
