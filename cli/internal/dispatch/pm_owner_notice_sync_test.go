package dispatch

import (
	"context"
	"testing"
	"time"
)

func TestPMOwnerNoticeUploadBudgetSurvivesDesktopDeliveryAndRestart(t *testing.T) {
	ctx := context.Background()
	b := Binding{Home: t.TempDir(), Scope: "scope", Revision: "revision", AgentID: "owner"}
	job := Job{Kind: "pm_push", Scope: b.Scope, Revision: b.Revision, Message: &Message{Conversation: "42"}}
	notice := PMOwnerNotice{Topic: "order", Change: "ready", Summary: "Materials are ready", NeedsAction: true}
	if err := SavePMNotice(ctx, b, job, notice); err != nil {
		t.Fatal(err)
	}
	if err := DeliverPMDigest(ctx, b, "cycle", func([]PMNoticeEntry) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := SavePMNotice(ctx, b, job, notice); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	var id string
	for attempt := 1; attempt <= 4; attempt++ {
		item, err := ClaimPMNoticeUpload(ctx, b, now)
		if err != nil || item.Attempts != attempt || item.Summary != notice.Summary || item.ConversationID != "42" {
			t.Fatalf("attempt %d: %+v %v", attempt, item, err)
		}
		if attempt == 1 {
			id = item.ResultID
		}
		if id == "" || item.ResultID != id {
			t.Fatal("retry changed the idempotency key")
		}
		item, err = ClaimPMNoticeUpload(ctx, b, now)
		if err != nil || item.ResultID != "" {
			t.Fatalf("unexpected immediate retry: %+v %v", item, err)
		}
		now = now.Add(time.Minute)
	}
	item, err := ClaimPMNoticeUpload(ctx, b, now.Add(time.Hour))
	if err != nil || item.ResultID != "" {
		t.Fatalf("exceeded first attempt plus three retries: %+v %v", item, err)
	}

	notice.Change = "updated"
	if err := SavePMNotice(ctx, b, job, notice); err != nil {
		t.Fatal(err)
	}
	item, err = ClaimPMNoticeUpload(ctx, b, now)
	if err != nil || item.ResultID == "" || item.ResultID == id || item.Attempts != 1 {
		t.Fatalf("next result: %+v %v", item, err)
	}
	if err := FinishPMNoticeUpload(ctx, b, item); err != nil {
		t.Fatal(err)
	}
	if item, err := ClaimPMNoticeUpload(ctx, b, now.Add(time.Hour)); err != nil || item.ResultID != "" {
		t.Fatalf("accepted result replayed: %+v %v", item, err)
	}
}
