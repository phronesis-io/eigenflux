package install

import "testing"

// TestDeriveChannel verifies explicit UTM attribution takes precedence; when
// absent, a platform click identifier determines the paid channel.
func TestDeriveChannel(t *testing.T) {
	cases := []struct {
		name, entry, src, click, bilibili, twclid, gclid, xingtu, oceanengine, want string
	}{
		{"dedicated oceanengine entry survives missing macros", "oceanengine", "", "", "", "", "", "", "", "oceanengine"},
		{"dedicated Bilibili entry survives missing track id", "bilibili", "", "", "", "", "", "", "", "bilibili"},
		{"dedicated entry wins conflicting UTM", "oceanengine", "xiaohongshu", "cid", "", "", "", "", "", "oceanengine"},
		{"explicit xhs", "", "xiaohongshu", "cid", "", "", "", "", "", "xiaohongshu"},
		{"alias xhs", "", "xhs", "", "", "", "", "", "", "xiaohongshu"},
		{"Bilibili track id infers Bilibili", "", "", "", "track123", "", "", "", "", "bilibili"},
		{"oceanengine clickid infers oceanengine", "", "", "", "", "", "", "", "oe123", "oceanengine"},
		{"xingtu clickid infers xingtu", "", "", "", "", "", "", "xt123", "", "xingtu"},
		{"click id infers xhs", "", "", "cid123", "", "", "", "", "", "xiaohongshu"},
		{"twclid infers twitter", "", "", "", "", "tw123", "", "", "", "twitter"},
		{"gclid infers google", "", "", "", "", "", "gcl123", "", "", "google"},
		{"Bilibili track id wins stale source", "", "weibo", "cid", "track123", "", "gcl123", "xt123", "oe123", "bilibili"},
		{"explicit source still wins other platform ids", "", "weibo", "cid", "", "", "gcl123", "xt123", "oe123", "weibo"},
		{"no signal is unknown", "", "", "", "", "", "", "", "", "unknown"},
	}
	for _, c := range cases {
		if got := deriveChannel(c.entry, c.src, c.click, c.bilibili, c.twclid, c.gclid, c.xingtu, c.oceanengine); got != c.want {
			t.Errorf("%s: deriveChannel(...)=%q want %q", c.name, got, c.want)
		}
	}
}
