package install

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeBilibiliTrackID(t *testing.T) {
	if got := normalizeBilibiliTrackID("  bili-track-123  "); got != "bili-track-123" {
		t.Fatalf("trimmed track id = %q", got)
	}
	if got := normalizeBilibiliTrackID("bad\ntrack"); got != "" {
		t.Fatalf("control-character track id must be rejected, got %q", got)
	}
	if got := normalizeBilibiliTrackID(strings.Repeat("x", bilibiliTrackIDMaxLength+1)); got != "" {
		t.Fatal("oversized track id must be rejected")
	}
}

func TestBilibiliEventTimestamp(t *testing.T) {
	tok := &Token{CopiedAt: 1604888786102, ReportedAt: 1604888888000}
	if got := bilibiliEventTimestamp(tok, bilibiliEventFormSubmit); got != tok.CopiedAt {
		t.Fatalf("FORM_SUBMIT timestamp = %d, want %d", got, tok.CopiedAt)
	}
	if got := bilibiliEventTimestamp(tok, bilibiliEventClueValid); got != tok.ReportedAt {
		t.Fatalf("CLUE_VALID timestamp = %d, want %d", got, tok.ReportedAt)
	}
}

func TestReportBilibiliConversion(t *testing.T) {
	for _, eventType := range []string{bilibiliEventFormSubmit, bilibiliEventClueValid} {
		t.Run(eventType, func(t *testing.T) {
			var method, path, trackID, gotType, gotTime, gotIP string
			withBilibiliServer(t, func(w http.ResponseWriter, r *http.Request) {
				method, path = r.Method, r.URL.Path
				trackID = r.URL.Query().Get("track_id")
				gotType = r.URL.Query().Get("conv_type")
				gotTime = r.URL.Query().Get("conv_time")
				gotIP = r.URL.Query().Get("client_ip")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "message": "success"})
			}, func() {
				code, err := reportBilibiliConversion("track-from-landing", eventType, 1604888786102, "203.0.113.8")
				if err != nil || code != 0 {
					t.Fatalf("code=%d err=%v", code, err)
				}
			})
			if method != http.MethodGet || path != "/conv/api/conversion/ad/cb/v1" {
				t.Fatalf("method=%q path=%q", method, path)
			}
			if trackID != "track-from-landing" || gotType != eventType || gotTime != "1604888786102" || gotIP != "203.0.113.8" {
				t.Fatalf("track_id=%q conv_type=%q conv_time=%q client_ip=%q", trackID, gotType, gotTime, gotIP)
			}
		})
	}
}

func TestReportBilibiliConversionErrors(t *testing.T) {
	withBilibiliServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream unavailable"))
	}, func() {
		code, err := reportBilibiliConversion("track", bilibiliEventFormSubmit, 1, "")
		if code != http.StatusBadGateway || err == nil {
			t.Fatalf("HTTP error code=%d err=%v", code, err)
		}
	})

	oldHTTP := bilibiliHTTP
	bilibiliHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})}
	defer func() { bilibiliHTTP = oldHTTP }()
	code, err := reportBilibiliConversion("track", bilibiliEventClueValid, 1, "")
	if code != -2 || err == nil {
		t.Fatalf("transport error code=%d err=%v", code, err)
	}
}

func TestReportBilibiliConversionResponseValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		wantCode int
	}{
		{"empty success", "", 0},
		{"whitespace success", " \n", 0},
		{"explicit success", `{"code":0}`, 0},
		{"rejection", `{"code":1001,"message":"rejected"}`, 1001},
		{"html", `<html>upstream error</html>`, -2},
		{"truncated json", `{"code":`, -2},
		{"missing code", `{"message":"error"}`, -2},
		{"null code", `{"code":null}`, -2},
		{"null response", `null`, -2},
		{"string code", `{"code":"0"}`, -2},
		{"oversized response", `{"message":"` + strings.Repeat("x", 4096) + `","code":0}`, -2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withBilibiliServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}, func() {
				code, err := reportBilibiliConversion("track", bilibiliEventFormSubmit, 1, "")
				if code != tc.wantCode || (err != nil) != (tc.wantCode != 0) {
					t.Fatalf("code=%d err=%v, want code=%d", code, err, tc.wantCode)
				}
			})
		})
	}
}

func TestReportBilibiliConversionRetriesTransientFailure(t *testing.T) {
	attempts := 0
	withBilibiliServer(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0})
	}, func() {
		oldDelays := bilibiliRetryDelays
		bilibiliRetryDelays = []time.Duration{0, 0, 0}
		defer func() { bilibiliRetryDelays = oldDelays }()
		code, err := reportBilibiliConversionWithRetry("track", bilibiliEventFormSubmit, 1, "")
		if code != 0 || err != nil {
			t.Fatalf("retry result code=%d err=%v", code, err)
		}
	})
	if attempts != 3 {
		t.Fatalf("attempts=%d, want 3", attempts)
	}
}

func TestBilibiliCallbackColsAndLease(t *testing.T) {
	if code, sent := bilibiliCallbackCols(bilibiliEventFormSubmit); code != "bilibili_form_submit_code" || sent != "bilibili_form_submit_sent_at" {
		t.Fatalf("FORM_SUBMIT columns = %s, %s", code, sent)
	}
	if code, sent := bilibiliCallbackCols(bilibiliEventClueValid); code != "bilibili_clue_valid_code" || sent != "bilibili_clue_valid_sent_at" {
		t.Fatalf("CLUE_VALID columns = %s, %s", code, sent)
	}
	if bilibiliCallbackSignalCol(bilibiliEventFormSubmit) != "copied_at" || bilibiliCallbackSignalCol(bilibiliEventClueValid) != "reported_at" {
		t.Fatal("Bilibili callback signal columns are incorrect")
	}
	now := int64(10 * 60 * 1000)
	if !bilibiliCallbackClaimable(0, now) || bilibiliCallbackClaimable(now-bilibiliCallbackLease.Milliseconds(), now) || !bilibiliCallbackClaimable(now-bilibiliCallbackLease.Milliseconds()-1, now) {
		t.Fatal("Bilibili callback lease boundaries are incorrect")
	}
}

func withBilibiliServer(t *testing.T, handler http.HandlerFunc, run func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	oldBase, oldHTTP := bilibiliCallbackBase, bilibiliHTTP
	bilibiliCallbackBase = srv.URL + "/conv/api/conversion/ad/cb/v1"
	bilibiliHTTP = srv.Client()
	defer func() { bilibiliCallbackBase, bilibiliHTTP = oldBase, oldHTTP }()
	run()
}
