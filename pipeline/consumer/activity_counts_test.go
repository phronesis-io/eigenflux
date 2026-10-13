package consumer

import "testing"

func TestFeedPullImpressionQuantity(t *testing.T) {
	for _, tt := range []struct {
		detail string
		want   int64
	}{
		{`{"count":0}`, 0}, {`{"count":3}`, 3}, {`{"count":0,"impression_id":"365412762643333120","arm":"a1"}`, 0},
		{`{"count":3,"metadata":{"version":"v1"}}`, 3}, {`{}`, 1}, {``, 1}, {`{`, 1},
		{`{"count":null}`, 1}, {`{"count":-1}`, 1}, {`{"count":0.5}`, 1}, {`{"count":"3"}`, 1}, {`{"count":9223372036854775808}`, 1},
	} {
		if got := feedPullImpressionDelta(tt.detail); got != tt.want {
			t.Errorf("detail=%s delta=%d want=%d", tt.detail, got, tt.want)
		}
	}
}

func TestFeedbackQuantityAcceptsUnrelatedMetadata(t *testing.T) {
	if got := parseDetailInt(`{"kept":2,"count":3,"impression_id":"365412762643333120"}`, "kept"); got != 2 {
		t.Fatalf("kept=%d", got)
	}
	if got := parseDetailInt(`{"kept":-1}`, "kept"); got != 0 {
		t.Fatalf("negative quantity=%d", got)
	}
}
