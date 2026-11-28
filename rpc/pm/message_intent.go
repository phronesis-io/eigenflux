package main

import (
	"errors"
	"fmt"

	"eigenflux_server/kitex_gen/eigenflux/pm"
)

func validateMessageIntent(req *pm.SendPMReq) error {
	if req.MessageKind == nil {
		if req.ReplyToMessageId != nil {
			return errors.New("reply_to_message_id requires message_kind=reply")
		}
		return nil
	}
	switch req.GetMessageKind() {
	case "contact":
		if req.ReplyToMessageId != nil {
			return errors.New("contact must not have reply_to_message_id")
		}
	case "reply":
		if req.GetReplyToMessageId() <= 0 || req.GetConvId() <= 0 || req.GetItemId() > 0 {
			return errors.New("reply requires positive conv_id and reply_to_message_id, without item_id")
		}
	default:
		return errors.New("message_kind must be contact or reply")
	}
	return nil
}

// Preserve existing deduplication keys for old requests. Explicit intent is
// part of a new send's identity so distinct replies with identical text cannot
// inherit a receipt for a different target or contact action.
func intentTargetKind(req *pm.SendPMReq, kind string) string {
	if req.MessageKind == nil && req.ReplyToMessageId == nil {
		return kind
	}
	return fmt.Sprintf("%s:intent:%s:%d", kind, req.GetMessageKind(), req.GetReplyToMessageId())
}
