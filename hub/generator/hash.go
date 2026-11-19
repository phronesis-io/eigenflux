package generator

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"fluxspace.ai/hub/models"
)

// HashStr mirrors hashStr() in the frozen prototype fluxspace/_check.js.
// The JS original iterates UTF-16 code units via charCodeAt, so encoding runes
// to UTF-16 first is required to keep hashes identical for CJK titles.
// h *= 16777619 is the arithmetic equivalent of the prototype's shift-sum.
func HashStr(s string) string {
	var h uint32 = 0x811c9dc5
	for _, u := range utf16.Encode([]rune(s)) {
		h ^= uint32(u)
		h *= 16777619
	}
	return strconv.FormatUint(uint64(h), 36)
}

// Fingerprint mirrors fingerprintOf(): game type, theme, title, scene, clue
// keys, round titles, and sorted per-round action id sets. Two games that
// differ only in narrative skin but share structure still get distinct
// fingerprints; StructuralHash is the one that deliberately collides.
func Fingerprint(s *models.Skeleton) string {
	clueKeys := make([]string, 0, len(s.Rounds))
	titles := make([]string, 0, len(s.Rounds))
	actSets := make([]string, 0, len(s.Rounds))
	for _, r := range s.Rounds {
		if len(r.Reveal) > 0 {
			clueKeys = append(clueKeys, r.Reveal[0].Key)
		} else {
			clueKeys = append(clueKeys, "")
		}
		titles = append(titles, r.Title)
		ids := make([]string, 0, len(r.Actions))
		for _, a := range r.Actions {
			ids = append(ids, a.ID)
		}
		sort.Strings(ids)
		actSets = append(actSets, strings.Join(ids, "+"))
	}
	raw := strings.Join([]string{
		s.Type,
		s.ThemeID,
		s.Title,
		s.ThemeText,
		strings.Join(clueKeys, ","),
		strings.Join(titles, "/"),
		strings.Join(actSets, "/"),
	}, "|")
	return "fp_" + HashStr(raw)
}

// StructuralHash covers topology-only inputs (story 005 AC6): game type, goal
// graph topology, information distribution, round count, branch count, ending
// type, and resource kinds. Narrative text is intentionally excluded.
func StructuralHash(s *models.Skeleton) string {
	resourceKinds := make([]string, 0, len(s.Resources))
	for _, r := range s.Resources {
		resourceKinds = append(resourceKinds, r.Key)
	}
	sort.Strings(resourceKinds)
	raw := strings.Join([]string{
		s.Type,
		"goal_graph:" + goalTopology(s),
		"info:fragmented",
		strconv.Itoa(len(s.Rounds)),
		strconv.Itoa(len(s.Branches)),
		"ending:" + conditionKind(s.WinCondition),
		strings.Join(resourceKinds, "+"),
	}, "|")
	return "sh_" + HashStr(raw)
}

func goalTopology(s *models.Skeleton) string {
	// MVP deduction uses a linear metric-race goal graph; the conjunctive win
	// condition makes it a single AND-join node.
	if len(s.WinCondition.All) > 1 {
		return "and_join"
	}
	if len(s.WinCondition.Any) > 1 {
		return "or_branch"
	}
	return "linear"
}

func conditionKind(c models.Condition) string {
	if len(c.All) > 0 {
		return "all"
	}
	if len(c.Any) > 0 {
		return "any"
	}
	return "single"
}

// ContentHash pins the full generated content. Hash fields are blanked before
// marshalling so the value is stable regardless of assignment order.
func ContentHash(s *models.Skeleton) string {
	projection := *s
	projection.Fingerprint = ""
	projection.StructuralHash = ""
	projection.ContentHash = ""
	b, err := json.Marshal(&projection)
	if err != nil {
		// json.Marshal of this struct cannot fail: no channels, funcs or cycles.
		return "ch_unavailable"
	}
	return "ch_" + HashStr(string(b))
}
