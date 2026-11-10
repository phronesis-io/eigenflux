// Targeted suppression of the four reviewed historical summary incidents.
// Prepare/dry-run are read-only. Apply retains originals and queues only the
// existing completed-item projection path, never a fresh publication.
package main

import (
	"context"
	"crypto/sha256"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/itemdispatch"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"os"
	"time"
)

var approved = map[int64]string{376715300894146560: "body_only", 375607914615799808: "summary_original", 377856703162482688: "body_only", 377166422679224320: "body_only"}
var fields = []string{"summary", "summary_origin", "summary_zh", "title_zh", "summary_zh_literal", "title_zh_literal", "summary_zh_key", "title_zh_key"}

type plan struct {
	OperationID string         `json:"operation_id"`
	ItemID      int64          `json:"item_id,string"`
	Mode        string         `json:"mode"`
	SourceHash  string         `json:"source_hash"`
	Before      map[string]any `json:"before"`
	Reason      string         `json:"reason"`
	Evidence    string         `json:"evidence"`
}
type state struct {
	Content, Notes, SummaryRemediation string
	Status                             int
	Values                             map[string]any
}

func sourceHash(s state) string {
	b, _ := json.Marshal([]string{s.Content, s.Notes})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func read(tx *gorm.DB, id int64, lock bool) (state, error) {
	var r struct{ RawContent, RawNotes string }
	var s state
	q := tx.Table("raw_items").Where("item_id=?", id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if e := q.Take(&r).Error; e != nil {
		return s, e
	}
	row := map[string]any{}
	q = tx.Table("processed_items").Where("item_id=?", id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if e := q.Take(&row).Error; e != nil {
		return s, e
	}
	s.Content, s.Notes = r.RawContent, r.RawNotes
	s.Values = map[string]any{}
	for _, k := range fields {
		s.Values[k] = row[k]
	}
	s.SummaryRemediation, _ = row["summary_remediation"].(string)
	switch v := row["status"].(type) {
	case int16:
		s.Status = int(v)
	case int32:
		s.Status = int(v)
	case int64:
		s.Status = int(v)
	case int:
		s.Status = v
	}
	return s, nil
}
func equal(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func validate(p plan, s state) error {
	if approved[p.ItemID] != p.Mode || p.Mode == "" || p.OperationID == "" || p.Reason == "" || p.Evidence == "" {
		return errors.New("unapproved or incomplete repair plan")
	}
	if s.Status != 3 || p.SourceHash != sourceHash(s) {
		return errors.New("source/lifecycle precondition changed")
	}
	if len(p.Before) != len(fields) {
		return errors.New("incomplete before image")
	}
	for _, k := range fields {
		if _, ok := p.Before[k]; !ok {
			return errors.New("missing before field")
		}
	}
	return nil
}
func process(tx *gorm.DB, p plan, apply, revert bool) error {
	s, e := read(tx, p.ItemID, apply)
	if e != nil {
		return e
	}
	if e = validate(p, s); e != nil {
		return e
	}
	var audit struct {
		OperationID, Mode, SourceHash string
		ItemID                        int64
		BeforeValues                  []byte
		ReversedAt                    *int64
	}
	result := tx.Table("summary_remediation_audit").Where("operation_id=?", p.OperationID).Take(&audit)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return result.Error
	}
	exists := result.Error == nil
	if exists {
		var saved map[string]any
		if e = json.Unmarshal(audit.BeforeValues, &saved); e != nil {
			return e
		}
		if audit.ItemID != p.ItemID || audit.Mode != p.Mode || audit.SourceHash != p.SourceHash || !equal(saved, p.Before) {
			return errors.New("operation identity reused")
		}
	}
	if revert {
		if !exists {
			return errors.New("no activation to reverse")
		}
		if audit.ReversedAt != nil {
			if s.SummaryRemediation == "" && equal(s.Values, p.Before) {
				return nil
			}
			return errors.New("reversed state changed")
		}
		if s.SummaryRemediation != p.Mode || !equal(s.Values, suppressed(p)) {
			return errors.New("active repair changed; do not overwrite")
		}
		if !apply {
			return nil
		}
		values := map[string]any{"summary_remediation": ""}
		for k, v := range p.Before {
			values[k] = v
		}
		if e = tx.Table("processed_items").Where("item_id=?", p.ItemID).Updates(values).Error; e != nil {
			return e
		}
		if e = tx.Table("summary_remediation_audit").Where("operation_id=?", p.OperationID).Updates(map[string]any{"reversed_at": time.Now().UnixMilli(), "projection_verified_at": nil}).Error; e != nil {
			return e
		}
	} else {
		if exists {
			if audit.ReversedAt == nil && s.SummaryRemediation == p.Mode && equal(s.Values, suppressed(p)) {
				return queue(tx, p.ItemID, apply)
			}
			return errors.New("operation is reversed or state changed")
		}
		if s.SummaryRemediation != "" || !equal(s.Values, p.Before) {
			return errors.New("text or active remediation changed")
		}
		if !apply {
			return nil
		}
		var pending int64
		if e = tx.Table("item_publish_outbox").Where("item_id=?", p.ItemID).Count(&pending).Error; e != nil {
			return e
		}
		if pending != 0 {
			return errors.New("existing publication dispatch must drain before activation")
		}
		before, _ := json.Marshal(p.Before)
		if e = tx.Exec(`INSERT INTO summary_remediation_audit(operation_id,item_id,mode,source_hash,before_values,reason,evidence,activated_at) VALUES(?,?,?,?,?::jsonb,?,?,?)`, p.OperationID, p.ItemID, p.Mode, p.SourceHash, string(before), p.Reason, p.Evidence, time.Now().UnixMilli()).Error; e != nil {
			return e
		}
		if e = tx.Table("processed_items").Where("item_id=?", p.ItemID).Update("summary_remediation", p.Mode).Error; e != nil {
			return e
		}
	}
	return queue(tx, p.ItemID, apply)
}
func suppressed(p plan) map[string]any {
	v := map[string]any{}
	for k, x := range p.Before {
		v[k] = x
	}
	v["summary_zh"], v["summary_zh_literal"], v["summary_zh_key"] = "", "", ""
	if p.Mode == "body_only" {
		v["summary"], v["summary_origin"], v["title_zh"], v["title_zh_literal"], v["title_zh_key"] = "", "none", "", "", ""
	}
	return v
}
func queue(tx *gorm.DB, id int64, apply bool) error {
	if !apply {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&itemdispatch.Outbox{ItemID: id, CreatedAt: time.Now().UnixMilli()}).Error
}
func main() {
	path := flag.String("plan", "", "private reviewed JSON plan")
	prepare := flag.Bool("prepare", false, "write a private read-only plan")
	apply := flag.Bool("apply", false, "apply reviewed plan")
	revert := flag.Bool("revert", false, "explicitly reverse this operation")
	flag.Parse()
	if *path == "" || (*prepare && (*apply || *revert)) {
		panic("provide plan and mutually compatible operation")
	}
	cfg := config.Load()
	db, e := gorm.Open(postgres.New(postgres.Config{DSN: cfg.PgDSN, PreferSimpleProtocol: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	check(e)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db = db.WithContext(ctx)
	plans := []plan{}
	if *prepare {
		check(db.Transaction(func(tx *gorm.DB) error {
			if e := tx.Exec("SET TRANSACTION READ ONLY").Error; e != nil {
				return e
			}
			for id, mode := range approved {
				s, e := read(tx, id, false)
				if e != nil {
					return e
				}
				p := plan{fmt.Sprintf("summary-history-20261109-%d", id), id, mode, sourceHash(s), s.Values, "Verified platform summary/translation defect, 2026-11-09", fmt.Sprintf("https://www.eigenflux.ai/dashboard/broadcasts/%d", id)}
				if e = validate(p, s); e != nil {
					return e
				}
				plans = append(plans, p)
			}
			return nil
		}))
		b, e := json.MarshalIndent(plans, "", "  ")
		check(e)
		f, e := os.OpenFile(*path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		check(e)
		_, e = f.Write(b)
		check(e)
		check(f.Close())
		fmt.Println("Prepared four-record private plan; no production changes")
		return
	}
	b, e := os.ReadFile(*path)
	check(e)
	check(json.Unmarshal(b, &plans))
	if len(plans) != 4 {
		panic("exactly four approved records required")
	}
	seen := map[int64]bool{}
	check(db.Transaction(func(tx *gorm.DB) error {
		if !*apply {
			if e := tx.Exec("SET TRANSACTION READ ONLY").Error; e != nil {
				return e
			}
		}
		for _, p := range plans {
			if seen[p.ItemID] {
				return errors.New("duplicate plan item")
			}
			seen[p.ItemID] = true
			if e := process(tx, p, *apply, *revert); e != nil {
				return e
			}
		}
		return nil
	}))
	fmt.Printf("Validated four records; applied=%t reversed=%t; projection verification remains required\n", *apply, *revert)
}
func check(e error) {
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
