package dal

import (
	"eigenflux_server/pkg/reqinfo"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"eigenflux_server/pkg/runtimeidentity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrRuntimeReportSuperseded = errors.New("runtime report superseded")

// boundedObservationText discards invalid optional metadata independently, so
// one malformed header cannot roll back otherwise valid product/activity facts.
func boundedObservationText(value string, maxBytes int) string {
	if len(value) > maxBytes || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return ""
	}
	return strings.TrimSpace(value)
}

// RuntimeObservation contains facts from an authenticated Agent request. Mode is
// independent of Host. Explicit reports may declare the product version unknown;
// passive requests that omit a version retain the known version of that product.
type RuntimeObservation struct {
	Host       string
	Mode       string
	Model      string
	CLIVersion string
	ObservedAt int64
	Explicit   bool
	Active     bool
}

type RuntimeObservationResult struct {
	Outcome         string
	IdentityChanged bool
	ActivityChanged bool
}

// ObserveRuntime serializes metadata merges against explicit reports. A request
// timestamp is captured before running its handler, so delayed work cannot undo
// a newer report (including a mode-only report). Activity writes are bounded to
// one per minute per Agent unless metadata already needs a write.
func ObserveRuntime(db *gorm.DB, agentID int64, obs RuntimeObservation) (RuntimeObservationResult, error) {
	result := RuntimeObservationResult{Outcome: "unchanged"}
	if obs.Mode != "" && obs.Mode != "plugin" && obs.Mode != "skill" {
		return result, fmt.Errorf("mode must be plugin or skill")
	}
	obs.CLIVersion = boundedObservationText(obs.CLIVersion, 32)
	obs.Model = boundedObservationText(obs.Model, 128)
	// Product parts have independent VARCHAR(64) bounds; the legacy host is TEXT.
	obs.Host = boundedObservationText(obs.Host, 129)
	identity, hasIdentity := runtimeidentity.Parse(obs.Host)
	hasMetadata := hasIdentity || obs.Mode != "" || obs.Model != "" || obs.CLIVersion != ""
	if !hasMetadata && !obs.Active {
		return result, nil
	}
	if obs.ObservedAt == 0 {
		obs.ObservedAt = reqinfo.RequestStartedAt(db.Statement.Context)
	}
	if obs.ObservedAt == 0 {
		obs.ObservedAt = time.Now().UnixMilli()
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := GetSettings(tx, agentID); err != nil {
			return err
		}
		var cur AgentSettings
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, "agent_id = ?", agentID).Error; err != nil {
			return err
		}
		vals := map[string]interface{}{}
		stale := obs.ObservedAt < cur.RuntimeReportedAt || (!obs.Explicit && obs.ObservedAt == cur.RuntimeReportedAt)
		if hasMetadata && !stale {
			set := func(key, previous, next string) {
				if previous != next {
					vals[key] = next
					result.IdentityChanged = true
				}
			}
			mode := cur.Mode
			if obs.Mode != "" {
				mode = obs.Mode
				set("mode", cur.Mode, mode)
			}
			if hasIdentity {
				version := identity.Version
				if !obs.Explicit && version == "" && identity.Name == cur.RuntimeName {
					version = cur.RuntimeVersion
				}
				set("runtime_name", cur.RuntimeName, identity.Name)
				set("runtime_version", cur.RuntimeVersion, version)
				// Keep the legacy host projection coherent without deriving mode from it.
				host := ""
				if mode == "plugin" {
					host = identity.Name
					if version != "" {
						host += "/" + version
					}
				}
				set("client_host", cur.ClientHost, host)
			} else if obs.Mode == "skill" {
				set("client_host", cur.ClientHost, "")
			}
			if obs.Model != "" {
				set("model", cur.Model, obs.Model)
			}
			if obs.CLIVersion != "" {
				set("cli_version", cur.CLIVersion, obs.CLIVersion)
			}
			// Explicit reports always advance the fence, even when only mode was sent
			// or the same identity was reported again. Passive unchanged polls coalesce.
			if obs.Explicit || result.IdentityChanged || obs.ObservedAt-cur.RuntimeReportedAt >= time.Minute.Milliseconds() {
				fence := obs.ObservedAt
				if obs.Explicit && fence <= cur.RuntimeReportedAt {
					fence = cur.RuntimeReportedAt + 1
				}
				vals["runtime_reported_at"] = fence
			}
		} else if hasMetadata && stale {
			result.Outcome = "stale"
		}
		if obs.Active && obs.ObservedAt > cur.LastActivityAt && (result.IdentityChanged || cur.LastActivityAt == 0 || obs.ObservedAt-cur.LastActivityAt >= time.Minute.Milliseconds()) {
			vals["last_activity_at"] = obs.ObservedAt
			result.ActivityChanged = true
		}
		if len(vals) == 0 {
			return nil
		}
		if result.IdentityChanged {
			vals["updated_at"] = time.Now().UnixMilli()
			result.Outcome = "updated"
		}
		return tx.Model(&AgentSettings{}).Where("agent_id = ?", agentID).UpdateColumns(vals).Error
	})
	if err != nil {
		result = RuntimeObservationResult{Outcome: "failed"}
	}
	return result, err
}
