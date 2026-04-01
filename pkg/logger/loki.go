package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// LokiHandler wraps another slog.Handler, forwarding records to it while
// also batching and pushing JSON log entries to Loki's HTTP push API.
type LokiHandler struct {
	inner   slog.Handler
	service string
	lokiURL string
	client  *http.Client
	ch      chan lokiEntry
	done    chan struct{}
	flushed chan struct{}
	once    sync.Once
	bufPool *sync.Pool
	attrs   []slog.Attr // accumulated from WithAttrs calls (e.g. traceId, spanId)
}

type lokiEntry struct {
	Timestamp time.Time
	Line      string
	Level     string
}

type lokiPushBody struct {
	Streams []lokiStream `json:"streams"`
}

type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]string        `json:"values"`
}

const (
	lokiBatchSize     = 100
	lokiFlushInterval = 1 * time.Second
	lokiChanSize      = 1000
	lokiMaxRetries    = 2
	lokiRetryDelay    = 500 * time.Millisecond
)

func NewLokiHandler(inner slog.Handler, service string, lokiURL string) *LokiHandler {
	h := &LokiHandler{
		inner:   inner,
		service: service,
		lokiURL: lokiURL + "/loki/api/v1/push",
		client:  &http.Client{Timeout: 5 * time.Second},
		ch:      make(chan lokiEntry, lokiChanSize),
		done:    make(chan struct{}),
		flushed: make(chan struct{}),
		bufPool: &sync.Pool{New: func() any { return new(bytes.Buffer) }},
	}
	go h.batchLoop()
	return h
}

func (h *LokiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *LokiHandler) Handle(ctx context.Context, r slog.Record) error {
	// Forward to the inner handler (file + stdout) first.
	if err := h.inner.Handle(ctx, r); err != nil {
		return err
	}

	// Add accumulated handler attrs (traceId, spanId, service, etc.) to the
	// record so the Loki JSON line includes them. slog.Logger.Info() passes
	// context.Background(), so we can't rely on the ctx for trace extraction.
	// The record is a value copy, safe to mutate.
	if len(h.attrs) > 0 {
		r.AddAttrs(h.attrs...)
	}

	// Build the JSON line for Loki from the record.
	buf := h.bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	tmpHandler := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	_ = tmpHandler.Handle(ctx, r)

	entry := lokiEntry{
		Timestamp: r.Time,
		Line:      buf.String(),
		Level:     r.Level.String(),
	}
	h.bufPool.Put(buf)

	// Non-blocking send: drop if buffer full (tracing must never block).
	select {
	case h.ch <- entry:
	default:
	}

	return nil
}

func (h *LokiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	accumulated := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(accumulated, h.attrs)
	copy(accumulated[len(h.attrs):], attrs)
	return &LokiHandler{
		inner:   h.inner.WithAttrs(attrs),
		service: h.service,
		lokiURL: h.lokiURL,
		client:  h.client,
		ch:      h.ch,
		done:    h.done,
		flushed: h.flushed,
		bufPool: h.bufPool,
		attrs:   accumulated,
	}
}

func (h *LokiHandler) WithGroup(name string) slog.Handler {
	return &LokiHandler{
		inner:   h.inner.WithGroup(name),
		service: h.service,
		lokiURL: h.lokiURL,
		client:  h.client,
		ch:      h.ch,
		done:    h.done,
		flushed: h.flushed,
		bufPool: h.bufPool,
		attrs:   h.attrs,
	}
}

func (h *LokiHandler) batchLoop() {
	defer close(h.flushed)

	ticker := time.NewTicker(lokiFlushInterval)
	defer ticker.Stop()

	var batch []lokiEntry

	for {
		select {
		case entry, ok := <-h.ch:
			if !ok {
				if len(batch) > 0 {
					h.push(batch)
				}
				return
			}
			batch = append(batch, entry)
			if len(batch) >= lokiBatchSize {
				h.push(batch)
				batch = nil
			}
		case <-ticker.C:
			if len(batch) > 0 {
				h.push(batch)
				batch = nil
			}
		case <-h.done:
			// Drain remaining entries from channel.
			for {
				select {
				case entry := <-h.ch:
					batch = append(batch, entry)
				default:
					if len(batch) > 0 {
						h.push(batch)
					}
					return
				}
			}
		}
	}
}

func (h *LokiHandler) push(entries []lokiEntry) {
	byLevel := make(map[string][][]string)
	for _, e := range entries {
		ts := strconv.FormatInt(e.Timestamp.UnixNano(), 10)
		byLevel[e.Level] = append(byLevel[e.Level], []string{ts, e.Line})
	}

	var streams []lokiStream
	for level, values := range byLevel {
		streams = append(streams, lokiStream{
			Stream: map[string]string{
				"service": h.service,
				"level":   level,
			},
			Values: values,
		})
	}

	body := lokiPushBody{Streams: streams}
	data, err := json.Marshal(body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[loki] marshal error: %v\n", err)
		return
	}

	for attempt := 0; attempt <= lokiMaxRetries; attempt++ {
		resp, err := h.client.Post(h.lokiURL, "application/json", bytes.NewReader(data))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 300 {
				return
			}
		}
		if attempt < lokiMaxRetries {
			time.Sleep(lokiRetryDelay)
		}
	}
	fmt.Fprintf(os.Stderr, "[loki] failed to push %d entries after %d retries\n", len(entries), lokiMaxRetries+1)
}

// Flush drains the Loki buffer. Call on shutdown.
func (h *LokiHandler) Flush() {
	h.once.Do(func() {
		close(h.done)
		<-h.flushed
	})
}
