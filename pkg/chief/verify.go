package chief

import (
	"context"
	"strconv"
)

// VerifyAgentTransfer pulls recent agent_transfer entries on the receiver
// account and checks whether any of them match the supplied req. lookback
// bounds how many recent entries the ledger should return; pass <= 0 to use
// the ledger default.
func (c *Client) VerifyAgentTransfer(ctx context.Context, req VerifyReq, lookback int) (VerifyResult, error) {
	if req.TransferID == "" || req.ToAgentID == 0 {
		return VerifyResult{Reason: VerifyTransferNotFound}, nil
	}
	entries, err := c.ListEntries(ctx, strconv.FormatInt(req.ToAgentID, 10), "agent_transfer", lookback)
	if err != nil {
		return VerifyResult{}, err
	}
	for i := range entries {
		e := &entries[i]
		if e.Metadata.TransferID != req.TransferID {
			continue
		}
		if reason := matchEntry(e, req); reason != VerifyOK {
			return VerifyResult{Matched: false, Reason: reason, Entry: e}, nil
		}
		return VerifyResult{Matched: true, Reason: VerifyOK, Entry: e}, nil
	}
	return VerifyResult{Matched: false, Reason: VerifyTransferNotFound}, nil
}

func matchEntry(e *Entry, req VerifyReq) VerifyReason {
	if e.Metadata.FromAgentID != strconv.FormatInt(req.FromAgentID, 10) {
		return VerifyFromMismatch
	}
	if e.Metadata.ToAgentID != strconv.FormatInt(req.ToAgentID, 10) {
		return VerifyToMismatch
	}
	if e.Asset != req.Asset {
		return VerifyAssetMismatch
	}
	delta, err := strconv.ParseInt(e.AvailableDeltaAtomic, 10, 64)
	if err != nil || delta < req.MinAmountAtomic {
		return VerifyAmountShort
	}
	if e.Metadata.TransactionState != "SETTLED" {
		return VerifyNotSettled
	}
	return VerifyOK
}
