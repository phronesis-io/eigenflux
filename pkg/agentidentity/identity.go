// Package agentidentity owns the public, opaque identity exposed outside the
// EigenFlux trust boundary. Internal numeric agent IDs remain unchanged.
package agentidentity

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"eigenflux_server/pkg/metrics"

	"gorm.io/gorm"
)

const (
	ShortIDLength = 5
	alphabet      = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

var (
	shortIDPattern = regexp.MustCompile(`^[A-Za-z]{5}$`)
	ErrNotFound    = errors.New("agent identity not found")
)

// PublicIdentity is the stable identity returned in public and communication
// responses. Numeric Agent IDs are deliberately not part of this contract.
type PublicIdentity struct {
	AgentID     int64  `json:"-"`
	AgentName   string `json:"-"`
	ShortID     string `json:"short_id"`
	DisplayName string `json:"display_name"`
}

// ValidShortID reports whether value is exactly five case-sensitive ASCII
// letters. Callers must never normalize case.
func ValidShortID(value string) bool { return shortIDPattern.MatchString(value) }

// GenerateShortID uses rejection sampling so every character in the 52-letter
// alphabet has identical probability.
func GenerateShortID() (string, error) {
	shortID, err := generateShortID(rand.Reader)
	if err != nil {
		metrics.AgentShortIDGenerationFailureTotal.Inc()
	}
	return shortID, err
}

func generateShortID(reader io.Reader) (string, error) {
	result := make([]byte, 0, ShortIDLength)
	buf := make([]byte, 16)
	const unbiasedLimit = 256 - (256 % len(alphabet))
	for len(result) < ShortIDLength {
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", fmt.Errorf("generate short id: %w", err)
		}
		for _, value := range buf {
			if int(value) >= unbiasedLimit {
				continue
			}
			result = append(result, alphabet[int(value)%len(alphabet)])
			if len(result) == ShortIDLength {
				break
			}
		}
	}
	return string(result), nil
}

// DisplayName applies the public display fallback without exposing email or a
// numeric ID.
func DisplayName(agentName, shortID string) string {
	if value := strings.TrimSpace(agentName); value != "" {
		return value
	}
	if ValidShortID(shortID) {
		return "Agent #" + shortID
	}
	return "Agent"
}

// Lookup resolves one public short ID. Lookup is case-sensitive because the
// database column uses C collation and no normalization is performed here.
func Lookup(ctx context.Context, db *gorm.DB, shortID string) (int64, error) {
	if !ValidShortID(shortID) {
		metrics.AgentShortIDLookupTotal.WithLabelValues("not_found").Inc()
		return 0, ErrNotFound
	}
	var agentID int64
	result := db.WithContext(ctx).Raw(
		`SELECT agent_id FROM agents WHERE short_id = ? AND short_id IS NOT NULL`, shortID,
	).Scan(&agentID)
	if result.Error != nil {
		metrics.AgentShortIDLookupTotal.WithLabelValues("error").Inc()
		return 0, result.Error
	}
	if agentID <= 0 {
		metrics.AgentShortIDLookupTotal.WithLabelValues("not_found").Inc()
		return 0, ErrNotFound
	}
	metrics.AgentShortIDLookupTotal.WithLabelValues("found").Inc()
	return agentID, nil
}

// Get returns the public identity for one internal Agent ID.
func Get(ctx context.Context, db *gorm.DB, agentID int64) (PublicIdentity, error) {
	var row struct {
		ShortID   string `gorm:"column:short_id"`
		AgentName string `gorm:"column:agent_name"`
	}
	result := db.WithContext(ctx).Raw(
		`SELECT short_id, agent_name FROM agents WHERE agent_id = ?`, agentID,
	).Scan(&row)
	if result.Error != nil {
		return PublicIdentity{}, result.Error
	}
	if !ValidShortID(row.ShortID) {
		metrics.AgentShortIDMissingTotal.Inc()
		return PublicIdentity{}, ErrNotFound
	}
	return PublicIdentity{
		AgentID: agentID, AgentName: row.AgentName, ShortID: row.ShortID,
		DisplayName: DisplayName(row.AgentName, row.ShortID),
	}, nil
}

// GetBatch resolves public identities in one query. Missing/unbackfilled IDs
// are omitted so callers can fail closed rather than invent public handles.
func GetBatch(ctx context.Context, db *gorm.DB, agentIDs []int64) (map[int64]PublicIdentity, error) {
	result := make(map[int64]PublicIdentity)
	if len(agentIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		AgentID   int64  `gorm:"column:agent_id"`
		ShortID   string `gorm:"column:short_id"`
		AgentName string `gorm:"column:agent_name"`
	}
	if err := db.WithContext(ctx).Table("agents").
		Select("agent_id, short_id, agent_name").
		Where("agent_id IN ?", agentIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if ValidShortID(row.ShortID) {
			result[row.AgentID] = PublicIdentity{
				AgentID: row.AgentID, AgentName: row.AgentName, ShortID: row.ShortID,
				DisplayName: DisplayName(row.AgentName, row.ShortID),
			}
		} else {
			metrics.AgentShortIDMissingTotal.Inc()
		}
	}
	return result, nil
}
