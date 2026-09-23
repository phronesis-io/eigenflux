package need

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

var (
	ErrNotFound    = errors.New("need_input_not_found")
	ErrConflict    = errors.New("idempotency_conflict")
	ErrStaleIntent = errors.New("intent_revision_not_current")
)

type Record struct {
	NeedInputID    int64           `json:"need_input_id,string"`
	AgentID        int64           `json:"agent_id,string"`
	IntentID       int64           `json:"intent_id,string"`
	IntentVersion  int64           `json:"intent_version"`
	SchemaVersion  string          `json:"schema_version"`
	Input          json.RawMessage `json:"input"`
	IntentSnapshot json.RawMessage `json:"intent_snapshot"`
	Status         string          `json:"status"`
	CreatedAt      int64           `json:"created_at"`
	UpdatedAt      int64           `json:"updated_at"`
	NormalizedNeed *Projection     `json:"normalized_need,omitempty"`
}
type row struct {
	NeedInputID    int64 `gorm:"primaryKey"`
	AgentID        int64
	IntentID       int64
	IntentVersion  int64
	SchemaVersion  string
	Input          json.RawMessage `gorm:"type:jsonb"`
	IntentSnapshot json.RawMessage `gorm:"type:jsonb"`
	Status         string
	IdempotencyKey string
	RequestHash    string
	CreatedAt      int64
	UpdatedAt      int64
}

func (row) TableName() string { return "need_inputs" }
func (r row) record() Record {
	return Record{NeedInputID: r.NeedInputID, AgentID: r.AgentID, IntentID: r.IntentID, IntentVersion: r.IntentVersion, SchemaVersion: r.SchemaVersion, Input: r.Input, IntentSnapshot: r.IntentSnapshot, Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

type IDGenerator interface{ NextID() (int64, error) }
type Store struct {
	DB  *gorm.DB
	IDs IDGenerator
}

func canonical(raw []byte) ([]byte, string, error) {
	var value any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return nil, "", err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	s := sha256.Sum256(b)
	return b, hex.EncodeToString(s[:]), nil
}
func (s Store) Create(ctx context.Context, owner int64, key string, raw []byte, now int64) (Record, bool, error) {
	in, err := Decode(raw)
	if err != nil {
		return Record{}, false, err
	}
	if owner <= 0 {
		return Record{}, false, invalid("owner", "invalid")
	}
	intentID, intentVersion := in.IntentID, in.IntentVersion
	normalized, err := NormalizeBasic(in)
	if err != nil {
		return Record{}, false, err
	}
	if len(key) < 8 || len(key) > 128 || strings.TrimSpace(key) != key {
		return Record{}, false, invalid("idempotency_key", "require_8_to_128_printable_characters")
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return Record{}, false, invalid("idempotency_key", "printable_ascii_required")
		}
	}
	canon, hash, err := canonical(raw)
	if err != nil {
		return Record{}, false, err
	}
	var result row
	replay := false
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", owner).Error; err != nil {
			return err
		}
		if err := tx.Where("agent_id=? AND idempotency_key=?", owner, key).First(&result).Error; err == nil {
			if result.RequestHash != hash {
				return ErrConflict
			}
			replay = true
			// Upgrade inputs captured before synchronous normalization without
			// rerunning or replacing an already normalized/enriched result.
			if result.Status == "pending" || result.Status == "failed" {
				if _, err := s.insertProjection(tx, result, normalized, BasicNormalizerVersion, "", now); err != nil {
					return err
				}
				result.Status, result.UpdatedAt = "normalized", now
				return tx.Model(&row{}).Where("need_input_id = ?", result.NeedInputID).Updates(map[string]any{"status": result.Status, "updated_at": now}).Error
			}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var intent struct {
			Version           int64 `gorm:"column:version"`
			Status            string
			WatchFor          string
			TriggerWhen       string
			ActionInstruction string `gorm:"column:action_instruction"`
			ActionPolicy      string `gorm:"column:action_policy"`
			Priority          int16
		}
		if err := tx.Raw(`SELECT version,status,watch_for,trigger_when,action_instruction,action_policy,priority FROM agent_intent_actions WHERE agent_id=? AND intent_id=? FOR UPDATE`, owner, intentID).Scan(&intent).Error; err != nil {
			return err
		}
		if intent.Version == 0 || intent.Status != "active" || intent.Version != intentVersion {
			return ErrStaleIntent
		}
		snapshot, err := json.Marshal(map[string]any{"intent_id": strconv.FormatInt(intentID, 10), "intent_version": intent.Version, "watch_for": intent.WatchFor, "trigger_when": intent.TriggerWhen, "action_instruction": intent.ActionInstruction, "action_policy": intent.ActionPolicy, "priority": intent.Priority})
		if err != nil {
			return err
		}
		id, err := s.IDs.NextID()
		if err != nil {
			return err
		}
		result = row{NeedInputID: id, AgentID: owner, IntentID: intentID, IntentVersion: intentVersion, SchemaVersion: InputSchemaVersion, Input: canon, IntentSnapshot: snapshot, Status: "normalized", IdempotencyKey: key, RequestHash: hash, CreatedAt: now, UpdatedAt: now}
		if err := tx.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}).Create(&result).Error; err != nil {
			return err
		}
		_, err = s.insertProjection(tx, result, normalized, BasicNormalizerVersion, "", now)
		return err
	})
	if err != nil {
		return Record{}, false, err
	}
	record, err := s.Get(ctx, owner, result.NeedInputID)
	return record, replay, err
}
func (s Store) Get(ctx context.Context, owner, id int64) (Record, error) {
	var r row
	err := s.DB.WithContext(ctx).Where("agent_id=? AND need_input_id=?", owner, id).First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	records := []Record{r.record()}
	err = s.attachProjections(ctx, records)
	return records[0], err
}

type Page struct {
	Inputs     []Record `json:"need_inputs"`
	NextCursor string   `json:"next_cursor"`
}

func (s Store) List(ctx context.Context, owner, cursor, limit int64) (Page, error) {
	out := Page{Inputs: []Record{}}
	if limit < 1 || limit > 100 || cursor < 0 {
		return out, invalid("pagination", "invalid")
	}
	q := s.DB.WithContext(ctx).Where("agent_id=?", owner)
	if cursor > 0 {
		q = q.Where("need_input_id<?", cursor)
	}
	var rows []row
	if err := q.Order("need_input_id DESC").Limit(int(limit) + 1).Find(&rows).Error; err != nil {
		return out, err
	}
	if len(rows) > int(limit) {
		out.NextCursor = strconv.FormatInt(rows[limit-1].NeedInputID, 10)
		rows = rows[:limit]
	}
	for _, r := range rows {
		out.Inputs = append(out.Inputs, r.record())
	}
	return out, s.attachProjections(ctx, out.Inputs)
}
