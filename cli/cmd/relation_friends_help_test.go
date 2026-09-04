package cmd

import (
	"strings"
	"testing"
)

func TestRelationFriendsHelpDocumentsPaginationContract(t *testing.T) {
	help := strings.Join(strings.Fields(relationFriendsCmd.Long), " ")
	for _, want := range []string{
		"at most 100 friends per page",
		"pass the response's next_cursor to --cursor",
		"not a guarantee that more friends remain",
		"reaches total",
		`next_cursor is "0"`,
	} {
		if !strings.Contains(help, want) {
			t.Errorf("relation friends help missing %q", want)
		}
	}

	limitFlag := relationFriendsCmd.Flags().Lookup("limit")
	if limitFlag == nil || !strings.Contains(limitFlag.Usage, "max 100") {
		t.Fatalf("limit flag must document the maximum: %#v", limitFlag)
	}

	cursorFlag := relationFriendsCmd.Flags().Lookup("cursor")
	if cursorFlag == nil || !strings.Contains(cursorFlag.Usage, "next_cursor") {
		t.Fatalf("cursor flag must document next_cursor usage: %#v", cursorFlag)
	}
}
