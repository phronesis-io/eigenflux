// Package invite owns the stable invite codes used for KOL (达人) and channel
// (渠道) growth attribution. A code is long-lived and reusable — unlike the
// one-shot install_tokens ref minted per click — and is resolved into a fresh
// install token at entry time (/r/EFI-xxxxxx or /install?ic=EFI-xxxxxx), after
// which the existing install-attribution funnel applies unchanged.
package invite

import (
	"crypto/rand"
	"errors"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	// Prefix distinguishes stable invite codes from one-shot EF- install refs.
	Prefix = "EFI-"
	// codeLen random base62 chars: 62^6 ≈ 5.7e10 — ample for one code per user.
	codeLen = 6

	KindKOL     = "kol"     // bound to an agent; the agent's personal code
	KindChannel = "channel" // ops-created, not bound to an agent

	base62Charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	// maxByte keeps base62 sampling unbiased (largest multiple of 62 below 256).
	maxByte = 62 * 4
)

// Code maps to invite_codes: one stable, reusable attribution code. kind=kol
// rows are auto-created per agent (registration + lazy ensure + backfill);
// kind=channel rows are created by ops via scripts/invite_channel.
type Code struct {
	Code      string `gorm:"column:code;primaryKey"`
	Kind      string `gorm:"column:kind;not null"`
	AgentID   int64  `gorm:"column:agent_id;not null;default:0"`
	Name      string `gorm:"column:name;not null;default:''"`
	Note      string `gorm:"column:note;not null;default:''"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
}

func (Code) TableName() string { return "invite_codes" }

var codeRe = regexp.MustCompile(`^EFI-[0-9A-Za-z]{6}$`)

// ValidFormat is the cheap pre-DB guard for user-supplied invite codes.
func ValidFormat(code string) bool { return codeRe.MatchString(code) }

// NewCode returns a fresh invite code: "EFI-" + 6 unbiased base62 chars.
func NewCode() string {
	out := make([]byte, 0, codeLen)
	buf := make([]byte, codeLen)
	for len(out) < codeLen {
		if _, err := rand.Read(buf); err != nil {
			panic(err) // crypto/rand failure is unrecoverable (matches install.NewToken)
		}
		for _, b := range buf {
			if b >= maxByte {
				continue
			}
			out = append(out, base62Charset[int(b)%62])
			if len(out) == codeLen {
				break
			}
		}
	}
	return Prefix + string(out)
}

// IsInternalEmail reports whether an agent email belongs to an internal fleet
// account (bot/pgc). Internal accounts don't get invite codes — they never
// invite anyone and would only pollute the KOL leaderboard.
func IsInternalEmail(email string) bool {
	e := strings.ToLower(email)
	return strings.Contains(e, "bot.eigenflux") || strings.Contains(e, "pgc.eigenflux")
}

// GetByCode loads an invite code, returning (nil, nil) when it doesn't exist.
func GetByCode(db *gorm.DB, code string) (*Code, error) {
	var c Code
	err := db.Where("code = ?", code).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// EnsureForAgent returns the agent's KOL invite code, creating it on first use.
// Idempotent and race-safe: the partial unique index on (agent_id) WHERE
// kind='kol' makes concurrent creates collapse to one row; the loser re-reads.
func EnsureForAgent(db *gorm.DB, agentID int64) (*Code, error) {
	if agentID <= 0 {
		return nil, errors.New("invalid agent id")
	}
	for range 3 {
		var existing Code
		err := db.Where("kind = ? AND agent_id = ?", KindKOL, agentID).First(&existing).Error
		if err == nil {
			return &existing, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		c := &Code{
			Code:      NewCode(),
			Kind:      KindKOL,
			AgentID:   agentID,
			CreatedAt: time.Now().UnixMilli(),
		}
		if err := db.Create(c).Error; err == nil {
			return c, nil
		}
		// Insert lost either the per-agent race or a code collision — retry
		// (the next round re-reads, or draws a fresh code).
	}
	return nil, errors.New("failed to ensure invite code after retries")
}

// NormalizeChannelName canonicalizes an ops-supplied channel name (lowercased,
// trimmed, capped) so one channel can't split into two codes by casing.
func NormalizeChannelName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

// EnsureChannel returns the channel's invite code, creating it on first use.
// Idempotent by normalized name (partial unique index on (name) WHERE
// kind='channel'), so re-running ops scripts never splits a channel's data.
func EnsureChannel(db *gorm.DB, name, note string) (c *Code, created bool, err error) {
	name = NormalizeChannelName(name)
	if name == "" {
		return nil, false, errors.New("channel name must not be empty")
	}
	for range 3 {
		var existing Code
		qerr := db.Where("kind = ? AND name = ?", KindChannel, name).First(&existing).Error
		if qerr == nil {
			return &existing, false, nil
		}
		if !errors.Is(qerr, gorm.ErrRecordNotFound) {
			return nil, false, qerr
		}
		nc := &Code{
			Code:      NewCode(),
			Kind:      KindChannel,
			Name:      name,
			Note:      note,
			CreatedAt: time.Now().UnixMilli(),
		}
		if cerr := db.Create(nc).Error; cerr == nil {
			return nc, true, nil
		}
	}
	return nil, false, errors.New("failed to ensure channel code after retries")
}

// TokenChannel resolves the install_tokens.channel bucket an invite-code entry
// should record when the visit carries no explicit utm_source: the channel's
// own name, or "kol" for personal codes.
func (c *Code) TokenChannel() string {
	if c.Kind == KindChannel && c.Name != "" {
		return c.Name
	}
	return KindKOL
}
