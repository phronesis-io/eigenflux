package cmd

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/client"
)

func TestDashboardLinkTTL(t *testing.T) {
	if dashboardLinkTTL != 15*time.Minute {
		t.Fatalf("dashboardLinkTTL = %s, want 15m", dashboardLinkTTL)
	}
}

func TestConsoleV2UnavailableOnlyFallsBackOnNotFound(t *testing.T) {
	if !consoleV2Unavailable(&client.APIError{StatusCode: http.StatusNotFound}) {
		t.Fatal("Console V2 404 must permit the existing V1 dashboard fallback")
	}
	for _, err := range []error{
		&client.APIError{StatusCode: http.StatusUnauthorized},
		&client.APIError{StatusCode: http.StatusConflict},
		&client.APIError{StatusCode: http.StatusInternalServerError},
		errors.New("network unavailable"),
	} {
		if consoleV2Unavailable(err) {
			t.Fatalf("unexpected V1 fallback for %v", err)
		}
	}
}
