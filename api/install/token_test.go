package install

import "testing"

// TestDeriveChannel verifies explicit UTM attribution takes precedence; when
// absent, a platform click identifier determines the paid channel.
func TestDeriveChannel(t *testing.T) {
	cases := []struct {
		name                              string
		src, click, twclid, gclid, xingtu string
		want                              string
	}{
		{"explicit xhs", "xiaohongshu", "cid", "", "", "", "xiaohongshu"},
		{"alias xhs", "xhs", "", "", "", "", "xiaohongshu"},
		{"xingtu clickid infers xingtu", "", "", "", "", "xt123", "xingtu"},
		{"click id infers xhs", "", "cid123", "", "", "", "xiaohongshu"},
		{"twclid infers twitter", "", "", "tw123", "", "", "twitter"},
		{"gclid infers google", "", "", "", "gcl123", "", "google"},
		{"explicit source wins", "weibo", "cid", "", "gcl123", "xt123", "weibo"},
		{"no signal is unknown", "", "", "", "", "", "unknown"},
	}
	for _, c := range cases {
		if got := deriveChannel(c.src, c.click, c.twclid, c.gclid, c.xingtu); got != c.want {
			t.Errorf("%s: deriveChannel(%q,%q,%q,%q,%q)=%q want %q", c.name, c.src, c.click, c.twclid, c.gclid, c.xingtu, got, c.want)
		}
	}
}
