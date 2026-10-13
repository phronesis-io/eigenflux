package agentcardapi

import (
	"context"
	"github.com/cloudwego/hertz/pkg/app"
	"strings"
	"testing"
)

func TestRefreshFunnelValidation(t *testing.T) {
	id := strings.Repeat("a", 32)
	valid := []refreshFunnelEvent{
		{Stage: "poll_started", PollID: id, Cause: "feed_poll"},
		{Stage: "poll_succeeded", PollID: id, Cause: "feed_poll"},
		{Stage: "poll_failed", PollID: id, Cause: "feed_poll", FailureCode: "client_error"},
		{Stage: "refresh_dispatched", RunID: id, PollID: id, Cause: "feed_poll"},
		{Stage: "refresh_dispatched", RunID: id, Cause: "host_schedule"},
		{Stage: "refresh_attempt_failed", RunID: id, Cause: "manual", FailureCode: "version_conflict"},
	}
	for _, e := range valid {
		if !validRefreshFunnelEvent(e) {
			t.Fatalf("valid event rejected: %+v", e)
		}
	}
	for _, e := range []refreshFunnelEvent{
		{Stage: "poll_started", PollID: "private text", Cause: "feed_poll"},
		{Stage: "refresh_dispatched", Cause: "manual"},
		{Stage: "refresh_attempt_failed", RunID: id, Cause: "manual", FailureCode: "raw private error"},
		{Stage: "poll_started", PollID: id, Cause: "manual"},
		{Stage: "unknown", RunID: id, Cause: "manual"},
	} {
		if validRefreshFunnelEvent(e) {
			t.Fatalf("invalid event accepted: %+v", e)
		}
	}
	if safeRefreshRunID("unbounded private text") != "" {
		t.Fatal("unsafe run header accepted")
	}
}

func TestRefreshEventRejectsUnauthenticatedAndUnknownProperties(t *testing.T) {
	c := app.NewContext(0)
	RecordRefreshEvent(context.Background(), c)
	if c.Response.StatusCode() != 401 {
		t.Fatal("unauthenticated event accepted")
	}
	c = app.NewContext(0)
	c.Set("agent_id", int64(42))
	c.Request.SetBodyString(`{"stage":"poll_started","poll_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","cause":"feed_poll","agent_id":99}`)
	RecordRefreshEvent(context.Background(), c)
	if c.Response.StatusCode() != 400 {
		t.Fatal("unknown/client identity property accepted")
	}
}
