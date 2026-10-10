package desktopnotify

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestHelperInstallLockExcludesOtherAccountsAndReads(t *testing.T) {
	directory := t.TempDir()
	release, err := lockHelper(context.Background(), directory, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, exclusive := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := lockHelper(ctx, directory, exclusive)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("concurrent lock: %v", err)
		}
	}
	release()
	read, err := lockHelper(context.Background(), directory, false)
	if err != nil {
		t.Fatal(err)
	}
	defer read()
	otherRead, err := lockHelper(context.Background(), directory, false)
	if err != nil {
		t.Fatal(err)
	}
	otherRead()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := lockHelper(ctx, directory, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("upgrade raced a reader: %v", err)
	}
}

func TestHelperProcessMatchRequiresExactOwnedExecutable(t *testing.T) {
	path := "/Users/test/Library/Application Support/EigenFlux/Notifications/EigenFlux Notifications.app/Contents/MacOS/EigenFluxNotifications"
	input := "  777 " + path + "\n 888 " + path + ".other\n 999 /Other/EigenFluxNotifications\n1 " + path + "\ninvalid " + path
	if got := helperPIDs(input, path); !reflect.DeepEqual(got, []int{777}) {
		t.Fatalf("matched foreign or invalid processes: %v", got)
	}
}
