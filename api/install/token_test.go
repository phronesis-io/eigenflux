package install

import "testing"

// TestDeriveChannel: an explicit utm_source wins; when it's missing/unknown a
// platform click id is decisive (click_id ⇒ xiaohongshu, twclid ⇒ twitter), so
// 聚光 traffic that only carries a click_id is not logged as "unknown".
func TestDeriveChannel(t *testing.T) {
	cases := []struct {
		name              string
		src, click, twcid string
		want              string
	}{
		{"explicit xhs", "xiaohongshu", "cid", "", "xiaohongshu"},
		{"alias xhs", "xhs", "", "", "xiaohongshu"},
		{"alias redbook", "redbook", "", "", "xiaohongshu"},
		{"alias 小红书", "小红书", "", "", "xiaohongshu"},
		{"clickid infers xhs when source empty", "", "cid123", "", "xiaohongshu"},
		{"twclid infers twitter when source empty", "", "", "tw123", "twitter"},
		{"explicit source beats click inference", "weibo", "cid", "", "weibo"},
		{"no signal is unknown", "", "", "", "unknown"},
	}
	for _, c := range cases {
		if got := deriveChannel(c.src, c.click, c.twcid); got != c.want {
			t.Errorf("%s: deriveChannel(%q,%q,%q)=%q want %q", c.name, c.src, c.click, c.twcid, got, c.want)
		}
	}
}
