package need

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReopenRequestRequiresExactSelection(t *testing.T) {
	valid := `{"intent_id":"123","intent_version":1,"expected_capture_revision":0}`
	r, err := DecodeReopenCapture([]byte(valid))
	if err != nil || r.ExpectedCaptureRevision == nil || *r.ExpectedCaptureRevision != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	for _, raw := range []string{"null", "[]", "{}", valid + "{}", strings.Replace(valid, `"123"`, `"0123"`, 1), strings.Replace(valid, `"intent_version":1`, `"intent_version":0`, 1), strings.Replace(valid, `,"expected_capture_revision":0`, "", 1), strings.Replace(valid, `"expected_capture_revision":0`, `"expected_capture_revision":null`, 1), strings.Replace(valid, `"expected_capture_revision":0`, `"expected_capture_revision":-1`, 1), strings.Replace(valid, `"expected_capture_revision":0`, `"expected_capture_revision":9223372036854775807`, 1), strings.Replace(valid, `"expected_capture_revision":0`, `"expected_capture_revision":0,"expected_capture_revision":1`, 1), strings.Replace(valid, `"expected_capture_revision":0`, `"expected_capture_revision":0,"agent_id":"7"`, 1)} {
		if _, err := DecodeReopenCapture([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestCaptureRevisionKeepsInitialReviewHashCompatible(t *testing.T) {
	old := `{"intent_id":"123","intent_version":1,"outcome":"no_need","reason":"Local reminder only","inputs":[]}`
	r, err := DecodeCaptureReview([]byte(old))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r)
	if string(raw) != old {
		t.Fatalf("legacy normalized review changed: %s", raw)
	}
	for _, value := range []string{"-1", "1.5", `"1"`, "null"} {
		bad := strings.Replace(old, `"outcome":`, `"capture_revision":`+value+`,"outcome":`, 1)
		if _, err := DecodeCaptureReview([]byte(bad)); err == nil {
			t.Fatalf("accepted revision %s", value)
		}
	}
	current := strings.Replace(old, `"outcome":`, `"capture_revision":2,"outcome":`, 1)
	r, err = DecodeCaptureReview([]byte(current))
	if err != nil || r.CaptureRevision != 2 {
		t.Fatalf("%+v %v", r, err)
	}
}
