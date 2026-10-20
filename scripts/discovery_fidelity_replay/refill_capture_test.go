package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/rpc/sort/discovery"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestCaptureSeenMatchesServingRollingAuthority(t *testing.T) {
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer r.Close()
	ctx := context.Background()
	docs := []discovery.Document{}
	now := time.Now()
	for i := int64(1); i <= 1100; i++ {
		docs = append(docs, discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: i}, GroupID: i})
		day := 0
		owner := int64(7)
		switch i % 4 {
		case 1:
			day = 29
		case 2:
			day = 30
		case 3:
			owner = 8
		}
		if err := r.SAdd(ctx, bloomfilter.GetKeyForDate(now.AddDate(0, 0, -day)), fmt.Sprintf("%d:%d", owner, i)).Err(); err != nil {
			t.Fatal(err)
		}
		if err := r.SAdd(ctx, "impr:agent:7:groups", i).Err(); err != nil {
			t.Fatal(err)
		}
	}
	want, err := (&discovery.Source{Redis: r}).Seen(ctx, 7, docs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := captureSeen(ctx, r, 7, docs)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("capture changed owner, rolling boundary, or chunk behavior: %v", err)
	}
	keys := make([]string, 30)
	for i := range keys {
		keys[i] = bloomfilter.GetKeyForDate(now.AddDate(0, 0, -i))
	}
	historyScript, verificationScript := discovery.ReplayConsumptionReadScripts()
	for _, kind := range []string{"history", "seen"} {
		groups := []int64{}
		for _, d := range docs {
			groups = append(groups, d.GroupID)
		}
		raw, _ := json.Marshal(map[string]any{"owner": 7, "kind": kind, "keys": keys, "groups": groups, "history_script": historyScript, "verification_script": verificationScript})
		cmd := exec.Command("python3", "-c", "import json,sys;from slim_es import consumption;print(json.dumps(consumption(json.load(sys.stdin))))")
		cmd.Env = append(os.Environ(), "REDIS_ADDR="+mr.Addr(), "REDIS_PASSWORD=")
		cmd.Stdin = bytes.NewReader(raw)
		output, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		var result slimConsumption
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatal(err)
		}
		hits := map[int64]bool{}
		for _, id := range result.Confirmed {
			hits[id] = true
		}
		for _, d := range docs {
			if hits[d.GroupID] != want[d.Ref.Key()] {
				t.Fatalf("SSH %s bridge changed rolling authority", kind)
			}
		}
		if !result.Complete || len(result.History) != len(docs) {
			t.Fatalf("SSH %s bridge lost history", kind)
		}
	}
}

func TestCaptureUnionPreservesExclusionEvidence(t *testing.T) {
	ref := discovery.SourceRef{Type: discovery.Broadcast, ID: 1}
	union := map[string]discovery.Document{}
	mergeCaptureDocument(union, discovery.Document{Ref: ref, NeedExclusionText: true})
	mergeCaptureDocument(union, discovery.Document{Ref: ref, Version: "v1"})
	mergeCaptureDocument(union, discovery.Document{Ref: ref, Version: "v1", Text: "matching text"})
	mergeCaptureDocument(union, discovery.Document{Ref: ref, Version: "v2", Text: "different text"})
	d := union[ref.Key()]
	if !d.NeedExclusionText || d.Version != "v1" || d.Text != "matching text" {
		t.Fatalf("lost or mixed evidence: %+v", d)
	}
}

func TestSharedCaptureFactsFreezeProjectionAndClaims(t *testing.T) {
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer r.Close()
	ctx := context.Background()
	captureFacts.Lock()
	captureFacts.forward = map[string]map[string]string{}
	captureFacts.claims = map[int64]bool{}
	captureFacts.Unlock()
	d := discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 11}, GroupID: 11}
	key := "discovery:forward:broadcast:v1:11:item"
	mr.HSet(key, "data", "first")
	first, err := captureForward(ctx, r, []discovery.Document{d})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := captureClaims(ctx, r, []int64{11})
	if err != nil || claims[11] {
		t.Fatal("unexpected initial claim", err)
	}
	mr.HSet(key, "data", "later")
	mr.Set("sort:inject:claim:11", "1")
	next, err := captureForward(ctx, r, []discovery.Document{d})
	if err != nil || !reflect.DeepEqual(first, next) {
		t.Fatal("shared projection changed during capture", err)
	}
	claims, err = captureClaims(ctx, r, []int64{11})
	if err != nil || claims[11] {
		t.Fatal("shared initial claim changed during capture", err)
	}
	if err := bloomfilter.NewBloomFilter(r).Add(ctx, 7, []int64{11}); err != nil {
		t.Fatal(err)
	}
	seen, err := captureSeen(ctx, r, 7, []discovery.Document{d})
	if err != nil || !seen[d.Ref.Key()] {
		t.Fatal("owner consumption was incorrectly frozen with shared item facts", err)
	}
}
