package need

import (
	"fmt"
	"strings"
	"testing"
)

func TestCaptureReviewValidation(t *testing.T) {
	input := `{"schema_version":"need_input.v2","intent_id":"123","intent_version":1,"need_type":"agent","target":{"goal":"Find a reviewer"}}`
	valid := fmt.Sprintf(`{"intent_id":"123","intent_version":1,"outcome":"captured","inputs":[%s]}`, input)
	for _, raw := range []string{valid, `{"intent_id":"123","intent_version":1,"outcome":"no_need","reason":"No discovery target","inputs":[]}`, `{"intent_id":"123","intent_version":1,"outcome":"captured","inputs":[]}`} {
		if _, err := DecodeCaptureReview([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{
		"null", "[]", "{}", valid + "{}",
		strings.Replace(valid, `"intent_id":"123"`, `"intent_id":"0123"`, 1),
		strings.Replace(valid, `"intent_version":1`, `"intent_version":1,"intent_version":1`, 1), strings.Repeat(" ", MaxReviewBytes+1),
		strings.Replace(valid, `"intent_version":1`, `"intent_version":2`, 1),
		strings.Replace(valid, `"captured"`, `"no_need"`, 1),
		strings.Replace(valid, `"inputs":`, `"unknown":true,"inputs":`, 1),
		fmt.Sprintf(`{"intent_id":"123","intent_version":1,"outcome":"captured","inputs":[%s,%s]}`, input, input),
		`{"intent_id":"123","intent_version":1,"outcome":"no_need","reason":"  ","inputs":[]}`,
	} {
		if _, err := DecodeCaptureReview([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
