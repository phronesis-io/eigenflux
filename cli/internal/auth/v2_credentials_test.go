package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasV2CredentialsDistinguishesMissingRegularAndSymlink(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)

	has, err := HasV2Credentials("default")
	if err != nil || has {
		t.Fatalf("missing credentials: has=%v err=%v", has, err)
	}

	credentials := &V2Credentials{
		AccessToken:  "efv2a_access",
		RefreshToken: "efv2r_refresh",
		AgentID:      "agent-1",
	}
	if err := SaveV2Credentials("default", credentials); err != nil {
		t.Fatal(err)
	}
	has, err = HasV2Credentials("default")
	if err != nil || !has {
		t.Fatalf("regular credentials: has=%v err=%v", has, err)
	}

	path := v2CredentialsPath("default")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "credentials-target.json")
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if has, err = HasV2Credentials("default"); err == nil || has {
		t.Fatalf("symlink credentials: has=%v err=%v", has, err)
	}
}
