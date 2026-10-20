package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/rpc/sort/discovery"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
)

type slimWorker struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}
type slimPool struct {
	workers chan *slimWorker
	all     []*slimWorker
}

var captureSlim *slimPool

func shellLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func startSlimPool(host string) (*slimPool, error) {
	code, err := os.ReadFile("scripts/discovery_fidelity_replay/slim_es.py")
	if err != nil {
		return nil, err
	}
	p := &slimPool{workers: make(chan *slimWorker, 6)}
	for i := 0; i < 6; i++ {
		command := "sudo -n bash -c " + shellLiteral("set -a; . /etc/eigenflux/runtime.env; exec python3 -u -c "+shellLiteral(string(code)))
		cmd := exec.Command("ssh", "-C", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host, command)
		input, err := cmd.StdinPipe()
		if err != nil {
			p.Close()
			return nil, err
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			p.Close()
			return nil, err
		}
		cmd.Stderr = os.Stderr
		if err = cmd.Start(); err != nil {
			p.Close()
			return nil, err
		}
		worker := &slimWorker{cmd, input, bufio.NewReaderSize(output, 65536)}
		p.all = append(p.all, worker)
		p.workers <- worker
	}
	return p, nil
}
func (p *slimPool) Close() {
	for _, w := range p.all {
		_ = w.in.Close()
		_ = w.cmd.Process.Kill()
		_ = w.cmd.Wait()
	}
}
func (p *slimPool) Query(ctx context.Context, c discovery.Context, channel string, limit int, groups, ids []int64, at int64) ([]discovery.Document, error) {
	start := time.Now()
	defer func() {
		if os.Getenv("REFILL_CAPTURE_VERBOSE") == "1" {
			fmt.Fprintf(os.Stderr, "slim %s limit=%d ms=%d\n", channel, limit, time.Since(start).Milliseconds())
		}
	}()
	q, err := discovery.ReplayChannelQuery(c, channel, min(limit, 200), groups, ids, at)
	if err != nil {
		return nil, err
	}
	q["size"] = limit
	line, err := p.exchange(ctx, map[string]any{"query": q, "exclusion_text": len(c.Filters.ExcludeTerms) > 0})
	if err != nil {
		return nil, err
	}
	var response struct {
		Error string `json:"error"`
		Hits  []struct {
			ID, Author string
			Group      int64
			Score      float64
			Hash, Text string
			Slots      searchindex.Slots
		} `json:"hits"`
	}
	if err = json.Unmarshal(line, &response); err != nil {
		return nil, err
	}
	if response.Error != "" {
		return nil, fmt.Errorf("slim ES capture: %s", response.Error)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	docs := []discovery.Document{}
	for _, h := range response.Hits {
		id, err := strconv.ParseInt(h.ID, 10, 64)
		if err != nil {
			return nil, err
		}
		author, err := strconv.ParseInt(h.Author, 10, 64)
		if err != nil {
			return nil, err
		}
		d := discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: id}, AuthorID: author, GroupID: h.Group, Version: h.Hash, Text: h.Text, Slots: h.Slots}
		if channel == "lexical" {
			d.Lexical = h.Score
		} else {
			score := h.Score
			d.DenseScore = &score
		}
		docs = append(docs, d)
	}
	return docs, nil
}

func (p *slimPool) exchange(ctx context.Context, request any) ([]byte, error) {
	var w *slimWorker
	select {
	case w = <-p.workers:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { p.workers <- w }()
	b, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err = w.in.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	line, err := w.out.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return line, nil
}

type slimConsumption struct {
	History, Confirmed    []int64
	Complete, Unavailable bool
	Error                 string
}

func (p *slimPool) consumption(ctx context.Context, owner int64, kind string, groups []int64) (slimConsumption, error) {
	history, verification := discovery.ReplayConsumptionReadScripts()
	now := time.Now()
	keys := make([]string, bloomfilter.BloomFilterLookbackDays)
	for i := range keys {
		keys[i] = bloomfilter.GetKeyForDate(now.AddDate(0, 0, -i))
	}
	raw, err := p.exchange(ctx, map[string]any{"kind": kind, "owner": owner, "keys": keys, "groups": groups, "history_script": history, "verification_script": verification})
	var result slimConsumption
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	if result.Error != "" {
		return result, fmt.Errorf("slim consumption capture: %s", result.Error)
	}
	return result, nil
}
