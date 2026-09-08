package install

import (
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func TestJoinDocumentRoutesFirstConnectionToOnboardingSkill(t *testing.T) {
	// The entry delegates installation and onboarding to the current canonical
	// document while carrying each visitor's origin and attribution separately.
	for _, origin := range []string{
		"https://www.eigenflux.ai",
		"https://www.eigenflux.net",
		"https://eigenflux.pro",
		"https://www.phronesis.studio",
	} {
		t.Run(origin, func(t *testing.T) {
			doc := renderJoinDoc("EF-1234abcd", origin)
			for _, required := range []string{
				"https://cdn.eigenflux.ai/skills/latest/install.md",
				"skills/install.md",
				"Installer origin: `" + origin + "`",
				"Referral code: `EF-1234abcd`",
			} {
				if !strings.Contains(doc, required) {
					t.Errorf("join document is missing %q", required)
				}
			}
			for _, forbidden := range []string{
				"{BASE}",
				"{REF}",
				"curl -fsSL",
				"--ref",
				"Ask the user for confirmation",
				"After confirmation",
				"eigenflux agent provision",
				"eigenflux auth login",
				"ef-onboarding",
				"Every Console handoff starts at Step 1",
				"recurring trigger",
				"Attention Prefill",
				"references/onboarding-v2.md",
				"Mandatory Join Route",
				"poll Feed, create or upload Attention",
			} {
				if strings.Contains(doc, forbidden) {
					t.Errorf("join document duplicates installation or onboarding instruction %q", forbidden)
				}
			}
		})
	}
}

func TestServeRefRejectsInvalidReferralWithoutCaching(t *testing.T) {
	h := server.New()
	h.GET("/r/:ref", serveRef)

	for _, ref := range []string{"not-a-ref", "EF-short", "EF-1234abcdextra"} {
		t.Run(ref, func(t *testing.T) {
			// Invalid refs exit before touching the database or attribution services.
			resp := ut.PerformRequest(h.Engine, http.MethodGet, "/r/"+ref, nil).Result()
			if resp.StatusCode() != http.StatusBadRequest {
				t.Fatalf("invalid ref status = %d, want %d", resp.StatusCode(), http.StatusBadRequest)
			}
			if got := string(resp.Header.Peek("Cache-Control")); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if got := string(resp.Header.ContentType()); got != "text/markdown; charset=utf-8" {
				t.Errorf("Content-Type = %q, want markdown", got)
			}
			if !strings.Contains(string(resp.Body()), "Invalid referral code") {
				t.Errorf("invalid ref response is missing validation message: %s", resp.Body())
			}
		})
	}
}
