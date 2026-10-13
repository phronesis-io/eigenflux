package main

import (
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"time"
)

type historyCell struct {
	AgentID        int64  `json:"agent_id,string"`
	Lane           string `json:"lane"`
	Basis          string `json:"basis"`
	Deliveries     int64  `json:"deliveries"`
	PositiveEvents int64  `json:"positive_events"`
}

func exportHistory(db *gorm.DB, dir string) {
	b, err := os.ReadFile(filepath.Join(dir, "cohort.json"))
	must(err)
	var cohort []struct{ AgentID int64 }
	must(json.Unmarshal(b, &cohort))
	zone, err := time.LoadLocation("Asia/Shanghai")
	must(err)
	now := time.Now()
	day := now.In(zone)
	// Seven complete delivery days, with at least one day for feedback to mature.
	hi := time.Date(day.Year(), day.Month(), day.Day()-1, 0, 0, 0, 0, zone)
	lo := hi.AddDate(0, 0, -7)
	all := []historyCell{}
	for _, a := range cohort {
		var cells []historyCell
		cache := filepath.Join(dir, fmt.Sprintf("history-%d.json", a.AgentID))
		if b, e := os.ReadFile(cache); e == nil {
			must(json.Unmarshal(b, &cells))
			all = append(all, cells...)
			continue
		}
		must(db.Raw(`WITH deliveries AS MATERIALIZED (
 SELECT r.agent_id,r.item_id,r.impression_id,min(r.served_at) served_at,
 min(CASE WHEN lower(p.email) LIKE '%@pgc.eigenflux.one' OR lower(p.email) LIKE '%@bot.eigenflux.one' THEN 'pgc' ELSE 'ugc' END) lane,
 min(CASE r.item_features #>> '{search,context,input_origin}' WHEN 'need_input' THEN 'need' WHEN 'agent_context' THEN 'profile' WHEN 'friend' THEN 'friend' WHEN 'baseline' THEN 'baseline' ELSE 'unknown' END) basis,
 count(*) copies
 FROM replay_logs r LEFT JOIN raw_items i USING(item_id) LEFT JOIN agents p ON p.agent_id=i.author_agent_id
 WHERE r.agent_id=? AND r.served_at>=? AND r.served_at<? AND r.delivered IS TRUE
 AND r.source_kind='broadcast' AND r.request_mode<>'search'
 GROUP BY r.agent_id,r.item_id,r.impression_id), matched AS (
 SELECT d.*,coalesce(f.n,0) positives FROM deliveries d LEFT JOIN LATERAL (
 SELECT count(*) n FROM feedback_logs f WHERE f.impression_id=d.impression_id
 AND f.agent_id=d.agent_id AND f.item_id=d.item_id AND f.feedback_at>=d.served_at
 AND f.feedback_at<? AND f.score IN(1,2) AND d.copies=1 AND d.impression_id<>'')f ON true)
 SELECT agent_id,lane,basis,count(*) deliveries,sum(positives) positive_events FROM matched GROUP BY agent_id,lane,basis`, a.AgentID, lo.UnixMilli(), hi.UnixMilli(), now.UnixMilli()).Scan(&cells).Error)
		save(cache, cells)
		all = append(all, cells...)
		fmt.Printf("history Agent %d: %d cells\n", a.AgentID, len(cells))
	}
	save(filepath.Join(dir, "feedback-history.json"), map[string]any{"delivery_start": lo.Format(time.RFC3339), "delivery_end": hi.Format(time.RFC3339), "feedback_cutoff": now.Format(time.RFC3339), "cells": all})
}

type observedCell struct {
	AgentID        int64  `json:"agent_id,string"`
	Lane           string `json:"lane"`
	FeedbackEvents int64  `json:"feedback_events"`
	PositiveEvents int64  `json:"positive_events"`
}

func exportObservedFeedback(db *gorm.DB, dir string) {
	b, err := os.ReadFile(filepath.Join(dir, "cohort.json"))
	must(err)
	var cohort []struct{ AgentID int64 }
	must(json.Unmarshal(b, &cohort))
	b, err = os.ReadFile(filepath.Join(dir, "feedback-history.json"))
	must(err)
	var bounds struct {
		Start string `json:"delivery_start"`
		End   string `json:"delivery_end"`
	}
	must(json.Unmarshal(b, &bounds))
	lo, err := time.Parse(time.RFC3339, bounds.Start)
	must(err)
	hi, err := time.Parse(time.RFC3339, bounds.End)
	must(err)
	all := []observedCell{}
	for _, a := range cohort {
		var rows []observedCell
		must(db.Raw(`SELECT f.agent_id,CASE WHEN lower(p.email) LIKE '%@pgc.eigenflux.one' OR lower(p.email) LIKE '%@bot.eigenflux.one' THEN 'pgc' ELSE 'ugc' END lane,count(*) feedback_events,count(*) FILTER(WHERE f.score IN(1,2)) positive_events
 FROM feedback_logs f LEFT JOIN raw_items i USING(item_id) LEFT JOIN agents p ON p.agent_id=i.author_agent_id
 WHERE f.agent_id=? AND f.feedback_at>=? AND f.feedback_at<? AND f.score IN(-1,0,1,2)
 GROUP BY f.agent_id,lane`, a.AgentID, lo.UnixMilli(), hi.UnixMilli()).Scan(&rows).Error)
		all = append(all, rows...)
	}
	save(filepath.Join(dir, "feedback-observed.json"), map[string]any{"start": bounds.Start, "end": bounds.End, "cells": all})
	fmt.Printf("observed feedback: %d Agent/source cells\n", len(all))
}
