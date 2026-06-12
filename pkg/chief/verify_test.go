package chief

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const settledEntry = `{"entries":[
  {"entryId":"e1","entryType":"agent_transfer","agentId":"42","asset":"USDC","availableDeltaAtomic":"10000",
   "metadata":{"fromAgentId":"99","toAgentId":"42","transferId":"t_abc","transactionState":"SETTLED","txHash":"h","settlementRecordId":"s"},
   "createdAt":"2026-06-09T08:34:52Z"}
]}`

func verifyClient(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, 5*time.Second)
}

func baseReq() VerifyReq {
	return VerifyReq{
		TransferID:      "t_abc",
		FromAgentID:     99,
		ToAgentID:       42,
		Asset:           "USDC",
		MinAmountAtomic: 10000,
	}
}

func TestVerify_OK(t *testing.T) {
	c := verifyClient(t, settledEntry)
	res, err := c.VerifyAgentTransfer(context.Background(), baseReq(), 50)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !res.Matched {
		t.Fatalf("expected match, reason=%s", res.Reason)
	}
	if res.Entry == nil || res.Entry.Metadata.TransferID != "t_abc" {
		t.Fatalf("entry %+v", res.Entry)
	}
}

func TestVerify_TransferNotFound(t *testing.T) {
	c := verifyClient(t, `{"entries":[]}`)
	res, err := c.VerifyAgentTransfer(context.Background(), baseReq(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched || res.Reason != VerifyTransferNotFound {
		t.Fatalf("reason %s", res.Reason)
	}
}

func TestVerify_AmountShort(t *testing.T) {
	c := verifyClient(t, settledEntry)
	req := baseReq()
	req.MinAmountAtomic = 20000
	res, err := c.VerifyAgentTransfer(context.Background(), req, 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched || res.Reason != VerifyAmountShort {
		t.Fatalf("reason %s", res.Reason)
	}
}

func TestVerify_NotSettled(t *testing.T) {
	pending := `{"entries":[{"entryId":"e1","entryType":"agent_transfer","agentId":"42","asset":"USDC","availableDeltaAtomic":"10000","metadata":{"fromAgentId":"99","toAgentId":"42","transferId":"t_abc","transactionState":"PENDING"}}]}`
	c := verifyClient(t, pending)
	res, err := c.VerifyAgentTransfer(context.Background(), baseReq(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched || res.Reason != VerifyNotSettled {
		t.Fatalf("reason %s", res.Reason)
	}
}

func TestVerify_FromMismatch(t *testing.T) {
	c := verifyClient(t, settledEntry)
	req := baseReq()
	req.FromAgentID = 7777
	res, err := c.VerifyAgentTransfer(context.Background(), req, 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched || res.Reason != VerifyFromMismatch {
		t.Fatalf("reason %s", res.Reason)
	}
}

func TestVerify_AssetMismatch(t *testing.T) {
	c := verifyClient(t, settledEntry)
	req := baseReq()
	req.Asset = "USDT"
	res, err := c.VerifyAgentTransfer(context.Background(), req, 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched || res.Reason != VerifyAssetMismatch {
		t.Fatalf("reason %s", res.Reason)
	}
}
