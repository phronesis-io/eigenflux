package config

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
)

// SlopAuthorInRollout selects a stable publisher cohort, shared by every worker
// and offline replay. Invalid shares fail closed; 10000 means all publishers.
func SlopAuthorInRollout(authorID int64, bps int) bool {
	if authorID <= 0 || bps <= 0 || bps > 10000 {
		return false
	}
	h := sha256.Sum256([]byte("ugc-slop-v021:" + strconv.FormatInt(authorID, 10)))
	return int(binary.BigEndian.Uint64(h[:8])%10000) < bps
}
