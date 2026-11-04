package config

import "testing"

func TestCommissionRPCPort(t *testing.T) {
	t.Setenv("COMMISSION_RPC_PORT", "18889")
	if got := Load().CommissionRPCPort; got != 18889 {
		t.Fatalf("got port %d", got)
	}
	t.Setenv("COMMISSION_RPC_PORT", "invalid")
	if got := Load().CommissionRPCPort; got != 8889 {
		t.Fatalf("got default port %d", got)
	}
}
