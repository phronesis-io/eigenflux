package agti

import (
	"testing"
	"time"
)

func TestIPLimiter(t *testing.T) {
	l := newIPLimiter(3, 50*time.Millisecond)
	for i := 0; i < 3; i++ {
		if !l.Allow("1.1.1.1") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if l.Allow("1.1.1.1") {
		t.Fatal("4th request in window should be limited")
	}
	if !l.Allow("2.2.2.2") {
		t.Fatal("other IP must not be affected")
	}
	time.Sleep(60 * time.Millisecond)
	if !l.Allow("1.1.1.1") {
		t.Fatal("new window should allow again")
	}
}
