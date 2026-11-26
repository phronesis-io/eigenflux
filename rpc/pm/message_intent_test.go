package main

import (
	"context"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/pm"
)

func TestMessageIntentSurvivesThriftAndSeparatesSendIdentity(t *testing.T) {
	kind, conv, target := "reply", int64(20), int64(30)
	req := &pm.SendPMReq{SenderId: 1, Content: "answer", ConvId: &conv, MessageKind: &kind, ReplyToMessageId: &target}
	wire := make([]byte, req.BLength())
	req.FastWrite(wire)
	var decoded pm.SendPMReq
	if _, err := decoded.FastRead(wire); err != nil {
		t.Fatal(err)
	}
	if decoded.GetMessageKind() != kind || decoded.GetReplyToMessageId() != target {
		t.Fatalf("intent lost: %+v", decoded)
	}
	if err := validateMessageIntent(&decoded); err != nil {
		t.Fatal(err)
	}
	first := intentTargetKind(&decoded, "conversation")
	other := int64(31)
	decoded.ReplyToMessageId = &other
	if first == intentTargetKind(&decoded, "conversation") {
		t.Fatal("distinct reply targets share send identity")
	}
	if got := intentTargetKind(&pm.SendPMReq{}, "conversation"); got != "conversation" {
		t.Fatalf("legacy identity changed: %s", got)
	}
}

func TestSendPMRejectsInvalidIntentBeforeDependencies(t *testing.T) {
	reply, contact, unknown := "reply", "contact", "guess"
	conv, target, zero, item := int64(20), int64(30), int64(0), int64(40)
	for _, req := range []*pm.SendPMReq{
		{ReplyToMessageId: &target},
		{MessageKind: &unknown},
		{MessageKind: &contact, ReplyToMessageId: &target},
		{MessageKind: &reply, ConvId: &conv},
		{MessageKind: &reply, ReplyToMessageId: &target},
		{MessageKind: &reply, ConvId: &conv, ReplyToMessageId: &zero},
		{MessageKind: &reply, ConvId: &conv, ReplyToMessageId: &target, ItemId: &item},
	} {
		req.Content = "answer"
		req.SenderId = 1
		resp, err := (&PMServiceImpl{}).SendPM(context.Background(), req)
		if err != nil || resp.BaseResp.Code != 400 {
			t.Fatalf("invalid intent reached dependencies: %+v %v", resp, err)
		}
	}
}
