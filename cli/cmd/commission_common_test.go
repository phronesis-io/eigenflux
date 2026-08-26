package cmd

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestTableOutputPreservesWorkflowStateVerbatim(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	t.Cleanup(func() { os.Stdout = original })
	if err := printTableJSON([]byte(`{"withdrawal":{"state":"settlement_pending"}}`)); err != nil {
		t.Fatal(err)
	}
	_ = write.Close()
	out, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "settlement_pending") {
		t.Fatalf("output=%q", out)
	}
}
