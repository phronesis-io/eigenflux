package dispatch

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPMOwnerNoticeUploadKeepsLegacySpoolReadable(t *testing.T) {
	ctx := context.Background()
	b := Binding{Home: t.TempDir(), Scope: "scope", Revision: "revision", AgentID: "owner"}
	job := Job{Kind: "pm_push", Scope: b.Scope, Revision: b.Revision, Message: &Message{Conversation: "42"}}
	notice := PMOwnerNotice{Topic: "order", Change: "ready", Summary: "Materials are ready"}
	if err := SavePMNotice(ctx, b, job, notice); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(b.Home, "watch", "pm-notices-"+journalID(b.Scope, b.Revision)+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := QueuePMNoticeUpload(ctx, b, job, notice); err != nil {
		t.Fatal(err)
	}
	item, err := ClaimPMNoticeUpload(ctx, b, time.Now())
	if err != nil || item.Attempts != 1 {
		t.Fatalf("expected first upload: %+v %v", item, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Freeze the released reader's schema so future edits to pmNoticeState
	// cannot make incompatible changes pass this check.
	var legacy struct {
		Pending                  []PMNoticeEntry `json:"pending"`
		History                  []PMNoticeEntry `json:"history"`
		Seen                     []string        `json:"seen"`
		Cycles                   []string        `json:"cycles"`
		LegacyDesktopUnconfirmed []string        `json:"desktop_unconfirmed,omitempty"`
	}
	if err := decodeStrictObject(after, &legacy); err != nil || len(legacy.Pending) != 1 {
		t.Fatalf("legacy reader lost its pending notice: %v, %+v", err, legacy.Pending)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("sync worker changed the reminder spool")
	}
}

func TestPMOwnerNoticeUploadBudgetSurvivesDesktopDeliveryAndRebind(t *testing.T) {
	ctx := context.Background()
	b := Binding{Home: t.TempDir(), Scope: "scope", Revision: "revision", AgentID: "owner", Endpoint: "https://example.test"}
	job := Job{Kind: "pm_push", Scope: b.Scope, Revision: b.Revision, Message: &Message{Conversation: "42"}}
	notice := PMOwnerNotice{Topic: "order", Change: "ready", Summary: "Materials are ready", NeedsAction: true}
	if err := QueuePMNoticeUpload(ctx, b, job, notice); err != nil {
		t.Fatal(err)
	}
	if err := SavePMNotice(ctx, b, job, notice); err != nil {
		t.Fatal(err)
	}
	if err := DeliverPMDigest(ctx, b, "cycle", func([]PMNoticeEntry) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := QueuePMNoticeUpload(ctx, b, job, notice); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	var id string
	for attempt := 1; attempt <= 4; attempt++ {
		if err := QueuePMNoticeUpload(ctx, b, job, notice); err != nil {
			t.Fatal(err)
		}
		item, err := ClaimPMNoticeUpload(ctx, b, now)
		if err != nil || item.Attempts != attempt || item.Summary != notice.Summary || item.ConversationID != "42" {
			t.Fatalf("attempt %d: %+v %v", attempt, item, err)
		}
		if attempt == 1 {
			id = item.ResultID
			otherAccount := b
			otherAccount.Scope = "another-account"
			otherEndpoint := b
			otherEndpoint.Endpoint = "https://another.example.test"
			for _, other := range []Binding{otherAccount, otherEndpoint} {
				if unexpected, err := ClaimPMNoticeUpload(ctx, other, now.Add(time.Minute)); err != nil || unexpected.ResultID != "" {
					t.Fatalf("upload crossed an account or endpoint boundary: %+v %v", unexpected, err)
				}
			}
			b.Revision = "rebound"
			job.Revision = b.Revision
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
	if err := QueuePMNoticeUpload(ctx, b, job, notice); err != nil {
		t.Fatal(err)
	}
	item, err := ClaimPMNoticeUpload(ctx, b, now.Add(time.Hour))
	if err != nil || item.ResultID != "" {
		t.Fatalf("exceeded first attempt plus three retries: %+v %v", item, err)
	}

	notice.Change = "updated"
	if err := QueuePMNoticeUpload(ctx, b, job, notice); err != nil {
		t.Fatal(err)
	}
	item, err = ClaimPMNoticeUpload(ctx, b, now)
	if err != nil || item.ResultID == "" || item.ResultID == id || item.Attempts != 1 {
		t.Fatalf("next result: %+v %v", item, err)
	}
	if err := FinishPMNoticeUpload(ctx, b, item); err != nil {
		t.Fatal(err)
	}
	if err := QueuePMNoticeUpload(ctx, b, job, notice); err != nil {
		t.Fatal(err)
	}
	if item, err := ClaimPMNoticeUpload(ctx, b, now.Add(time.Hour)); err != nil || item.ResultID != "" {
		t.Fatalf("accepted result replayed: %+v %v", item, err)
	}
}
