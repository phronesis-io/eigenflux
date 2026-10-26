package main

import (
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestConflictingBackfillModesFailBeforeConnecting(t *testing.T) {
	if os.Getenv("TEST_BACKFILL_CONFLICT") == "1" {
		flag.CommandLine = flag.NewFlagSet("backfill", flag.ExitOnError)
		os.Args = []string{"backfill", "--history", "--compliance", "--days=2026-10-02"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestConflictingBackfillModesFailBeforeConnecting$")
	cmd.Env = append(os.Environ(), "TEST_BACKFILL_CONFLICT=1", "PG_DSN=invalid")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "--history and --compliance are mutually exclusive") {
		t.Fatalf("expected usage error before connecting: err=%v output=%s", err, output)
	}
}
