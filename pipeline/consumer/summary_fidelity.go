package consumer

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"eigenflux_server/pkg/config"
	"gorm.io/gorm"
)

type summaryRoute int

const (
	summaryRouteServer    summaryRoute = iota // server-written summary, no audit
	summaryRouteAudited                       // server-written summary + PGC audit
	summaryRoutePublisher                     // PGC publisher's own notes summary
)

// summaryRoute picks who writes the stored summary. Only an authoritative PGC
// identity may use its notes summary; UGC always keeps the server summary.
// A PGC item without a usable notes summary keeps the existing path.
func (c *ItemConsumer) summaryRoute(pgc bool, rawNotes string) (summaryRoute, string) {
	if !pgc {
		return summaryRouteServer, ""
	}
	if c.pgcPublisherSummary {
		if s, ok := publisherSummary(rawNotes); ok {
			return summaryRoutePublisher, s
		}
	}
	if c.summaryFidelity {
		return summaryRouteAudited, ""
	}
	return summaryRouteServer, ""
}

// maxPublisherSummaryRunes matches the PGC notes envelope bound.
const maxPublisherSummaryRunes = 280

// pgcNeutralSummary is PGC's last-resort placeholder; it carries no content,
// so the server keeps generating its own summary instead.
const pgcNeutralSummary = "Source-linked report; see the full broadcast for details."

// publisherSummary returns the one-line summary a PGC publisher put in its
// notes envelope. Missing, non-string, empty, placeholder or over-length
// values report false so the caller keeps the server-generated summary.
func publisherSummary(rawNotes string) (string, bool) {
	var notes struct {
		Summary *string `json:"summary"`
	}
	if json.Unmarshal([]byte(rawNotes), &notes) != nil || notes.Summary == nil {
		return "", false
	}
	s := strings.TrimSpace(*notes.Summary)
	if s == "" || s == pgcNeutralSummary || utf8.RuneCountInString(s) > maxPublisherSummaryRunes {
		return "", false
	}
	return s, true
}

func pgcSummaryPublisher(db *gorm.DB, authorID int64, suffixes []string) (bool, error) {
	var row struct{ Email string }
	if err := db.Table("agents").Select("email").Where("agent_id = ?", authorID).Take(&row).Error; err != nil {
		return false, err
	}
	return config.EmailMatchesAnySuffix(row.Email, suffixes), nil
}
