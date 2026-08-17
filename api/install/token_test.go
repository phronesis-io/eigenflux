package install

import "testing"

// TestDeriveChannel verifies explicit UTM attribution takes precedence; when
// absent, a platform click identifier determines the paid channel.
func TestDeriveChannel(t *testing.T) {
	cases := []struct {
		name                                                  string
		entry, src, click, twclid, gclid, xingtu, oceanengine string
		want                                                  string
	}{
		{"dedicated oceanengine entry survives missing macros", "oceanengine", "", "", "", "", "", "", "oceanengine"},
		{"dedicated entry wins conflicting UTM", "oceanengine", "xiaohongshu", "cid", "", "", "", "", "oceanengine"},
		{"explicit xhs", "", "xiaohongshu", "cid", "", "", "", "", "xiaohongshu"},
		{"alias xhs", "", "xhs", "", "", "", "", "", "xiaohongshu"},
		{"oceanengine clickid infers oceanengine", "", "", "", "", "", "", "oe123", "oceanengine"},
		{"xingtu clickid infers xingtu", "", "", "", "", "", "xt123", "", "xingtu"},
		{"click id infers xhs", "", "", "cid123", "", "", "", "", "xiaohongshu"},
		{"twclid infers twitter", "", "", "", "tw123", "", "", "", "twitter"},
		{"gclid infers google", "", "", "", "", "gcl123", "", "", "google"},
		{"explicit source wins", "", "weibo", "cid", "", "gcl123", "xt123", "oe123", "weibo"},
		{"no signal is unknown", "", "", "", "", "", "", "", "unknown"},
	}
	for _, c := range cases {
		if got := deriveChannel(c.entry, c.src, c.click, c.twclid, c.gclid, c.xingtu, c.oceanengine); got != c.want {
			t.Errorf("%s: deriveChannel(...)=%q want %q", c.name, got, c.want)
		}
	}
}
