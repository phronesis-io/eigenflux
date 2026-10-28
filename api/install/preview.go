package install

import (
	"encoding/json"
	"regexp"
	"strings"

	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/invite"
	"github.com/cloudwego/hertz/pkg/app"
)

// Match crawler identities, not platform names: their in-app browsers are
// real visits too. LarkUrl was observed minting tokens from Feishu previews.
// Match complete UA products, not substrings in app metadata or encoded blobs.
var previewUARe = regexp.MustCompile(`(?i)(?:^|[ \t;(])((?:[a-z][a-z0-9._-]{0,62})?(?:bot|crawler|spider)(?:-[a-z0-9_-]+)?|crawl|preview|slurp|headless(?:chrome)?|facebookexternalhit|facebookcatalog|embedly|vkshare|larkurl|google-inspectiontool|bingpreview|googlewebpreview|skypeuripreview|qqurlpreview|wechatpreview)(?:/[a-z0-9._-]+)?(?:$|[ \t;)])`)
var previewSnippetUARe = regexp.MustCompile(`(?i)(?:^|[ \t;(])\+?https?://developers\.google\.com/\+/web/snippet(?:$|[ \t;)])`)

func isPreviewBot(userAgent string) bool {
	return previewBotIdentity(userAgent) != ""
}

func previewBotIdentity(userAgent string) string {
	if match := previewUARe.FindStringSubmatch(userAgent); match != nil {
		return strings.ToLower(match[1])
	}
	if previewSnippetUARe.MatchString(userAgent) {
		return "google-snippet"
	}
	// WhatsApp's unfurler identifies itself without a browser UA.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(userAgent)), "whatsapp/") {
		return "whatsapp"
	}
	return ""
}

func isPreviewRequest(c *app.RequestContext) bool {
	return previewReason(c) != ""
}

func previewReason(c *app.RequestContext) string {
	if isPreviewBot(string(c.GetHeader("User-Agent"))) {
		return "crawler_user_agent"
	}
	for _, name := range []string{"Purpose", "Sec-Purpose", "X-Purpose", "X-Moz"} {
		value := strings.ToLower(string(c.GetHeader(name)))
		for _, part := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ';' || r == ',' || r == '=' || r == ' ' || r == '\t'
		}) {
			if part == "prefetch" || part == "preview" || part == "prerender" {
				return strings.ToLower(name) + ":" + part
			}
		}
	}
	return ""
}

// Preview events measure excluded requests, never landing rows or people.
// Keep request bodies and URL queries out of logs; only the bounded UA and
// syntactically valid attribution identifiers are retained.
func logPreview(c *app.RequestContext, stage, ref, inviteCode string) {
	event("install_preview_blocked", ref, "stage", stage,
		"invite_code", inviteCode, "reason", previewReason(c),
		"crawler_identity", previewBotIdentity(string(c.GetHeader("User-Agent"))),
		"user_agent", trunc(string(c.GetHeader("User-Agent")), 512))
}

func previewMintInviteCode(c *app.RequestContext) string {
	var body mintBody
	raw, _ := c.Body()
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	code := strings.TrimSpace(body.InviteCode)
	if invite.ValidFormat(code) || agentidentity.ValidShortID(code) {
		return code
	}
	return ""
}
