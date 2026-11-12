package govern

import (
	"fmt"
	"sort"
	"strings"
)

// Model-version consistency codes.
const (
	ModelConsistent     = "consistent"
	ModelMixedVersions  = "mixed_versions"
	ModelVersionAbsent  = "version_unknown"
	ModelNoParticipants = "no_participants"
)

// ModelUsage is one seat's declared model version.
type ModelUsage struct {
	SeatID       string `json:"seat_id"`
	CompanionID  string `json:"companion_id"`
	ModelVersion string `json:"model_version"`
	Official     bool   `json:"official"`
}

// ModelConsistency is the result of checking a match's declared versions.
type ModelConsistency struct {
	Consistent bool         `json:"consistent"`
	Code       string       `json:"code"`
	Versions   []string     `json:"versions"`
	Seats      []ModelUsage `json:"seats"`
	Reason     string       `json:"reason"`
}

// CheckModelVersions verifies that every non-official seat declares the same
// model version.
//
// Absence is a failure, not a pass: a match whose version was never recorded
// cannot be certified as consistent, and treating "unknown" as "fine" would
// make the check worthless exactly where it matters (S5's model-version
// consistency requirement, and ADR-0005's "do not invent metadata" rule).
// Official backfills are excluded because they are not a user's model choice.
func CheckModelVersions(usages []ModelUsage) ModelConsistency {
	result := ModelConsistency{Seats: usages, Versions: []string{}}

	participants := make([]ModelUsage, 0, len(usages))
	for _, u := range usages {
		if u.Official {
			continue
		}
		participants = append(participants, u)
	}
	if len(participants) == 0 {
		result.Code = ModelNoParticipants
		result.Consistent = true
		result.Reason = "no non-official seats; nothing to compare"
		return result
	}

	seen := make(map[string]bool, len(participants))
	missing := make([]string, 0)
	for _, u := range participants {
		if strings.TrimSpace(u.ModelVersion) == "" {
			missing = append(missing, u.SeatID)
			continue
		}
		if !seen[u.ModelVersion] {
			seen[u.ModelVersion] = true
			result.Versions = append(result.Versions, u.ModelVersion)
		}
	}
	sort.Strings(result.Versions)

	if len(missing) > 0 {
		result.Code = ModelVersionAbsent
		result.Consistent = false
		result.Reason = fmt.Sprintf("model version not recorded for seat(s) %s; consistency cannot be certified",
			strings.Join(missing, ", "))
		return result
	}
	if len(result.Versions) > 1 {
		result.Code = ModelMixedVersions
		result.Consistent = false
		result.Reason = fmt.Sprintf("seats declare %d different model versions: %s",
			len(result.Versions), strings.Join(result.Versions, ", "))
		return result
	}
	result.Code = ModelConsistent
	result.Consistent = true
	result.Reason = fmt.Sprintf("all %d seat(s) declare %s", len(participants), result.Versions[0])
	return result
}

// CheckRepriseModelChange enforces the GDD's reprise rule: a same-spec reprise
// uses the original version, and swapping the model is a declared change
// (`different_model`), never an accident.
func CheckRepriseModelChange(original, reprise ModelConsistency, declared bool) error {
	if !original.Consistent || !reprise.Consistent {
		return fmt.Errorf("both sides must be model-consistent before comparing: original %q, reprise %q",
			original.Code, reprise.Code)
	}
	same := len(original.Versions) == len(reprise.Versions)
	if same {
		for i := range original.Versions {
			if original.Versions[i] != reprise.Versions[i] {
				same = false
				break
			}
		}
	}
	if same {
		return nil
	}
	if !declared {
		return fmt.Errorf("model version changed from %s to %s without being declared as a different_model reprise",
			strings.Join(original.Versions, ","), strings.Join(reprise.Versions, ","))
	}
	return nil
}
