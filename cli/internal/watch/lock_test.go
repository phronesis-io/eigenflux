package watch

import "testing"

func TestAccountLockAndScope(t *testing.T) {
	home, err := CanonicalHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release, err := Acquire(home, "a")
	if err != nil {
		t.Fatal(err)
	}
	if unexpected, err := Acquire(home, "a"); err == nil {
		unexpected()
		t.Fatal("duplicate owner")
	}
	other, err := Acquire(home, "b")
	if err != nil {
		t.Fatal(err)
	}
	other()
	release()
	again, err := Acquire(home, "a")
	if err != nil {
		t.Fatal(err)
	}
	again()
	if Scope(home, "a", "1", "p") == Scope(home, "a", "2", "p") {
		t.Fatal("account collision")
	}
	if Scope(home, "a", "1", "p") == Scope(home+"2", "a", "1", "p") {
		t.Fatal("home collision")
	}
}
