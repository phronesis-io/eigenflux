package selfupdate

import (
	"context"
	"strings"
	"testing"
)

func TestObservationDistinguishesActualCheckAndSiblingAdoption(t *testing.T) {
	o, _, _, _ := fixture(t)
	started := 0
	o.OnAttempt = func() { started++ }
	r := Check(context.Background(), o)
	if !r.Attempted || r.Phase != "install" || started != 1 {
		t.Fatalf("installation observation: %+v started=%d", r, started)
	}
	r = Check(context.Background(), o)
	if r.Attempted || r.Phase != "adoption" || started != 1 {
		t.Fatalf("sibling adoption counted as install: %+v", r)
	}
	o.Version = "0.0.48"
	r = Check(context.Background(), o)
	if r.Attempted || r.Status != "deferred" || started != 1 {
		t.Fatalf("throttle counted as attempt: %+v", r)
	}
}
func TestObservationRetainsFailurePhase(t *testing.T) {
	o, _, bin, _ := fixture(t)
	*bin = []byte(strings.Repeat("x", len(*bin)))
	r := Check(context.Background(), o)
	if r.Status != "failed" || r.Phase != "verify" || !r.Attempted {
		t.Fatalf("checksum failure phase: %+v", r)
	}
}
