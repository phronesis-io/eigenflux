// Package commissionaccess restricts Commission discovery APIs to configured Agents.
package commissionaccess

import (
	"errors"
	"strconv"
	"strings"
)

var errInvalidAllowlist = errors.New("invalid COMMISSION_AGENT_ID_WHITELIST")

type Allowlist struct {
	enabled  bool
	agentIDs map[int64]struct{}
}

func New(enabled bool, raw string) (*Allowlist, error) {
	if !enabled {
		return &Allowlist{}, nil
	}

	agentIDs := make(map[int64]struct{})
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		agentID, err := strconv.ParseInt(entry, 10, 64)
		if err != nil || agentID <= 0 {
			return nil, errInvalidAllowlist
		}
		agentIDs[agentID] = struct{}{}
	}
	return &Allowlist{enabled: true, agentIDs: agentIDs}, nil
}

func (a *Allowlist) Allows(agentID int64) bool {
	if agentID <= 0 {
		return false
	}
	if a == nil || !a.enabled {
		return true
	}
	_, ok := a.agentIDs[agentID]
	return ok
}
