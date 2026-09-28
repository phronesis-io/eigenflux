package main

import (
	"context"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestOfficialFeedRescueSkipsNeedSearchBeforeAnySideEffect(t *testing.T) {
	cfg := &config.Config{EnableNeedSearch: true, EnableOfficialFeedRescue: true}
	// Nil dependencies ensure neither entry point touches Redis, DB, LLM or PM.
	require.NotPanics(t, func() { runOfficialFeedRescue(context.Background(), cfg, nil, nil) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); StartOfficialFeedRescue(ctx, cfg, nil, nil) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disabled rescue entered its periodic loop")
	}
}
