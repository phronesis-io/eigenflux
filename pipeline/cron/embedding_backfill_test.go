package main

import (
	"testing"
	"time"

	"eigenflux_server/pkg/config"
)

func TestEmbeddingBackfillSettingsFromConfig_Defaults(t *testing.T) {
	settings := embeddingBackfillSettingsFromConfig(nil)

	if settings.batchSize != defaultEmbBackfillBatchSize {
		t.Fatalf("batchSize=%d, want %d", settings.batchSize, defaultEmbBackfillBatchSize)
	}
	if settings.interval != defaultEmbBackfillInterval {
		t.Fatalf("interval=%v, want %v", settings.interval, defaultEmbBackfillInterval)
	}
	if settings.workers != defaultEmbBackfillWorkers {
		t.Fatalf("workers=%d, want %d", settings.workers, defaultEmbBackfillWorkers)
	}
	if settings.pause != defaultEmbBackfillPause {
		t.Fatalf("pause=%v, want %v", settings.pause, defaultEmbBackfillPause)
	}
}

func TestEmbeddingBackfillSettingsFromConfig_Overrides(t *testing.T) {
	cfg := &config.Config{
		EmbeddingBackfillBatchSize: 500,
		EmbeddingBackfillInterval:  "2m",
		EmbeddingBackfillWorkers:   6,
		EmbeddingBackfillPauseMs:   25,
	}

	settings := embeddingBackfillSettingsFromConfig(cfg)

	if settings.batchSize != 500 {
		t.Fatalf("batchSize=%d, want 500", settings.batchSize)
	}
	if settings.interval != 2*time.Minute {
		t.Fatalf("interval=%v, want %v", settings.interval, 2*time.Minute)
	}
	if settings.workers != 6 {
		t.Fatalf("workers=%d, want 6", settings.workers)
	}
	if settings.pause != 25*time.Millisecond {
		t.Fatalf("pause=%v, want %v", settings.pause, 25*time.Millisecond)
	}
}

func TestEmbeddingBackfillSettingsFromConfig_InvalidIntervalFallsBack(t *testing.T) {
	cfg := &config.Config{EmbeddingBackfillInterval: "not-a-duration"}

	settings := embeddingBackfillSettingsFromConfig(cfg)

	if settings.interval != defaultEmbBackfillInterval {
		t.Fatalf("interval=%v, want fallback %v", settings.interval, defaultEmbBackfillInterval)
	}
}

func TestBuildEmbeddingBackfillInput(t *testing.T) {
	got := buildEmbeddingBackfillInput("Agent bio", "AI,ML", "Singapore")
	want := "Agent bio\nAI, ML. Singapore"
	if got != want {
		t.Fatalf("buildEmbeddingBackfillInput()=%q, want %q", got, want)
	}
}

func TestBuildEmbeddingBackfillInput_HandlesMissingBio(t *testing.T) {
	got := buildEmbeddingBackfillInput("", "AI,ML", "Singapore")
	want := "AI, ML. Singapore"
	if got != want {
		t.Fatalf("buildEmbeddingBackfillInput()=%q, want %q", got, want)
	}
}
