package skills

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestReadSignedFileBusyWriterHasRetryableIdentity(t *testing.T) {
	dir := signedReadFixture(t)
	held, ok, err := acquireLock(filepath.Dir(dir))
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer held.Release()
	if _, err := ReadSignedFile(dir, "ef-commission", "references/dispatch.md", 4096); !errors.Is(err, ErrSyncInProgress) {
		t.Fatalf("busy writer lost typed retry evidence: %v", err)
	}
}
