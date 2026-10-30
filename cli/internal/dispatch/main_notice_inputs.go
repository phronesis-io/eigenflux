package dispatch

import "strconv"

const MainNoticeBuyerInputEvent = "order.inputs.owner_required.local.v1"

func (n MainNotice) IsBuyerInputRequest() bool { return n.Event == MainNoticeBuyerInputEvent }

// Correlate only the verified buyer request; never persist its private contents.
func BuyerInputMainNotice(scope, orderID string, version int64, requestID string) MainNotice {
	return MainNotice{ID: "inputs:" + journalID(scope, orderID, strconv.FormatInt(version, 10), requestID), OrderID: orderID, Version: version, Role: "buyer", Event: MainNoticeBuyerInputEvent, State: "awaiting_seller", InputRequestID: requestID}
}
