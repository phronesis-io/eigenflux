package config

import "testing"

func TestSlopPublisherRolloutStableBoundaries(t *testing.T) {
	// Fixed vectors protect the bucket assignment across deployments.
	for id, bucket := range map[int64]int{1: 1668, 2: 8273, 991: 9841, 123456789: 5248} {
		for repeat := 0; repeat < 3; repeat++ {
			if SlopAuthorInRollout(id, bucket) || !SlopAuthorInRollout(id, bucket+1) {
				t.Fatalf("publisher %d changed bucket %d", id, bucket)
			}
		}
		if !SlopAuthorInRollout(id, 10000) {
			t.Fatal("full rollout excluded a publisher")
		}
		for _, invalid := range []int{-1, 0, 10001} {
			if SlopAuthorInRollout(id, invalid) {
				t.Fatal("invalid/zero rollout must be off")
			}
		}
	}
	if SlopAuthorInRollout(0, 10000) || SlopAuthorInRollout(-1, 10000) {
		t.Fatal("invalid publisher admitted")
	}
}

func TestSlopRolloutConfig(t *testing.T) {
	t.Setenv("UGC_SLOP_ENABLED", "false")
	t.Setenv("UGC_SLOP_ROLLOUT_BPS", "")
	if c := Load(); c.UGCSlopEnabled || c.UGCSlopRolloutBPS != 10000 {
		t.Fatal("default contract changed")
	}
	t.Setenv("UGC_SLOP_ROLLOUT_BPS", "1000")
	if Load().UGCSlopRolloutBPS != 1000 {
		t.Fatal("canary share was not loaded")
	}
}
