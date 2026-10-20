package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// The production provider proxy stalls pgx's comment-only liveness ping.
// Capture connections retain the server-enforced read-only startup setting and
// use ordinary queries as their liveness check instead. No serving code uses it.
type captureConnector struct{ driver.Connector }

func (c captureConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return captureConn{conn}, nil
}

type captureConn struct{ driver.Conn }

func (c captureConn) ResetSession(context.Context) error {
	if v, ok := c.Conn.(driver.Validator); ok && !v.IsValid() {
		return driver.ErrBadConn
	}
	return nil
}
func (c captureConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	start := time.Now()
	if os.Getenv("REFILL_CAPTURE_VERBOSE") == "1" {
		fmt.Fprintf(os.Stderr, "pg query begin bytes=%d args=%d\n", len(q), len(args))
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
	if os.Getenv("REFILL_CAPTURE_VERBOSE") == "1" {
		fmt.Fprintf(os.Stderr, "pg query end ms=%d error=%t\n", time.Since(start).Milliseconds(), err != nil)
	}
	return rows, err
}
func (c captureConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func captureDB(dsn string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	return sql.OpenDB(captureConnector{stdlib.GetConnector(*cfg)}), nil
}

// Friend recall consumes IDs only, while its shared legacy ES reader otherwise
// transfers embeddings and full bodies. Narrowing projection preserves its
// filter/sort/cutoff; authority fields are still captured by Source.Hydrate.
type captureESTransport struct{ next http.RoundTripper }

func (t captureESTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil && strings.HasSuffix(r.URL.Path, "/_search") {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body.Close()
		var q map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if decoder.Decode(&q) == nil && q["_source"] == nil && q["sort"] != nil {
			q["_source"] = []string{"id"}
			raw, err = json.Marshal(q)
			if err != nil {
				return nil, err
			}
		}
		r = r.Clone(r.Context())
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
	}
	return t.next.RoundTrip(r)
}
