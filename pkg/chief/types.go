package chief

import "fmt"

// Entry mirrors a row from GET /ledger/entries.
// All numeric fields are returned by Kovaloop as strings to preserve precision.
type Entry struct {
	EntryID              string        `json:"entryId"`
	EntryType            string        `json:"entryType"`
	AgentID              string        `json:"agentId"`
	Asset                string        `json:"asset"`
	AvailableDeltaAtomic string        `json:"availableDeltaAtomic"`
	Reason               string        `json:"reason"`
	Metadata             EntryMetadata `json:"metadata"`
	CreatedAt            string        `json:"createdAt"`
}

type EntryMetadata struct {
	FromAgentID         string `json:"fromAgentId"`
	ToAgentID           string `json:"toAgentId"`
	CounterpartyAgentID string `json:"counterpartyAgentId"`
	TransferID          string `json:"transferId"`
	TxHash              string `json:"txHash"`
	SettlementRecordID  string `json:"settlementRecordId"`
	SettlementMode      string `json:"settlementMode"`
	TransactionState    string `json:"transactionState"`
}

type entriesEnvelope struct {
	Entries []Entry `json:"entries"`
}

// Account mirrors GET /ledger/accounts/{agent_id}.
type Account struct {
	AgentID         string `json:"agentId"`
	Asset           string `json:"asset"`
	AvailableAtomic string `json:"availableAtomic"`
}

// VerifyReq carries the order-side facts the verifier must match.
type VerifyReq struct {
	TransferID      string
	FromAgentID     int64
	ToAgentID       int64
	Asset           string
	MinAmountAtomic int64
}

// VerifyReason explains why a verification failed.
type VerifyReason string

const (
	VerifyOK               VerifyReason = ""
	VerifyTransferNotFound VerifyReason = "transfer_not_found"
	VerifyFromMismatch     VerifyReason = "from_mismatch"
	VerifyToMismatch       VerifyReason = "to_mismatch"
	VerifyAssetMismatch    VerifyReason = "asset_mismatch"
	VerifyAmountShort      VerifyReason = "amount_short"
	VerifyNotSettled       VerifyReason = "not_settled"
)

type VerifyResult struct {
	Matched bool
	Reason  VerifyReason
	Entry   *Entry
}

// ChiefError wraps any non-2xx HTTP response from the ledger.
type ChiefError struct {
	StatusCode int
	Detail     string `json:"detail"`
}

func (e *ChiefError) Error() string {
	return fmt.Sprintf("chief: status=%d detail=%s", e.StatusCode, e.Detail)
}
