package testaccountreset

import "testing"

func TestDeleteByQueryResult(t *testing.T) {
	// Shape of a production response: recent documents deleted, older ones in
	// read-only lifecycle indices refused.
	readOnly := `{"took":39,"timed_out":false,"total":3,"deleted":1,"version_conflicts":0,"failures":[
		{"index":"items-000026","id":"1","cause":{"type":"cluster_block_exception","reason":"index [items-000026] blocked by: [FORBIDDEN/8/index write (api)];"},"status":403},
		{"index":"items-000025","id":"2","cause":{"type":"cluster_block_exception","reason":"index [items-000025] blocked by: [FORBIDDEN/8/index write (api)];"},"status":403}]}`
	deleted, left, err := DeleteByQueryResult([]byte(readOnly))
	if err != nil || deleted != 1 || left != 2 {
		t.Fatalf("read-only indices: deleted=%d left=%d err=%v", deleted, left, err)
	}
	if deleted, left, err := DeleteByQueryResult([]byte(`{"deleted":4,"timed_out":false,"version_conflicts":0,"failures":[]}`)); err != nil || deleted != 4 || left != 0 {
		t.Fatalf("clean delete: deleted=%d left=%d err=%v", deleted, left, err)
	}
	for name, raw := range map[string]string{
		"other failure":    `{"deleted":0,"failures":[{"index":"items-000030","cause":{"type":"es_rejected_execution_exception","reason":"queue full"}}]}`,
		"metadata block":   `{"deleted":0,"failures":[{"index":"items-000030","cause":{"type":"cluster_block_exception","reason":"index [items-000030] blocked by: [FORBIDDEN/9/index metadata (api)];"}}]}`,
		"timed out":        `{"deleted":2,"timed_out":true,"failures":[]}`,
		"version conflict": `{"deleted":2,"version_conflicts":1,"failures":[]}`,
		"not json":         `<html>`,
	} {
		if _, _, err := DeleteByQueryResult([]byte(raw)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
