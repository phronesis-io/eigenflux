package recordsearch

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	search "eigenflux_server/kitex_gen/eigenflux/recordsearch"
	"eigenflux_server/pkg/reqinfo"

	"github.com/bytedance/gopkg/cloud/metainfo"
)

func TestValidateSearchBoundary(t *testing.T) {
	ctx := metainfo.WithPersistentValue(context.Background(), reqinfo.KeyAgentID, "1")
	valid := search.SearchReq{OwnerAgentId: 1, Query: "合同_%!", Limit: 10, Status: "open"}
	if got := Validate(ctx, &valid, "open"); got != nil {
		t.Fatal(got)
	}
	for _, mutate := range []func(*search.SearchReq){
		func(r *search.SearchReq) { r.OwnerAgentId = 2 },
		func(r *search.SearchReq) { r.OwnerAgentId = 0 },
		func(r *search.SearchReq) { r.Limit = 0 },
		func(r *search.SearchReq) { r.Limit = 51 },
		func(r *search.SearchReq) { r.Cursor = -1 },
		func(r *search.SearchReq) { r.Status = "invalid" },
		func(r *search.SearchReq) { r.Query = "" },
		func(r *search.SearchReq) { r.Query = " x " },
		func(r *search.SearchReq) { r.Query = "\x00" },
		func(r *search.SearchReq) { r.Query = "\xff" },
		func(r *search.SearchReq) { r.Query = strings.Repeat("中", 101) },
	} {
		r := valid
		mutate(&r)
		if Validate(ctx, &r, "open") == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	if got := Validate(context.Background(), &valid, "open"); got == nil || got.Code != 403 {
		t.Fatal(got)
	}
	if Validate(ctx, nil, "open") == nil {
		t.Fatal("nil request accepted")
	}
}

func TestLiteralAndExcerpt(t *testing.T) {
	pattern, id := Literal("合同_%!")
	if pattern != "%合同!_!%!!%" || id != 0 {
		t.Fatalf("%q %d", pattern, id)
	}
	for _, q := range []string{"001", "+1", "-1", "0", "1x", "9223372036854775808"} {
		if _, id := Literal(q); id != 0 {
			t.Fatalf("noncanonical ID: %q", q)
		}
	}
	if _, id := Literal("9007199254740993"); id != 9007199254740993 {
		t.Fatal(id)
	}
	text := strings.Repeat("前", 500) + "正文命中" + strings.Repeat("后", 500)
	got := Preview(text, "正文命中", 320)
	if !strings.Contains(got, "正文命中") || utf8.RuneCountInString(got) != 320 || !utf8.ValidString(got) {
		t.Fatal(got)
	}
}
