package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/config"
)

// PendingLogin carries the login context (email, referral code) across the
// two-step OTP flow: `auth login` writes it, `auth verify` consumes it. The
// verify response has no email field, so without this file the identity
// reported for install attribution would be empty whenever the agent drops
// the optional flags. Flags on verify override the stored values.
type PendingLogin struct {
	ChallengeID string `json:"challenge_id"`
	Email       string `json:"email,omitempty"`
	Ref         string `json:"ref,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

func pendingLoginPath() string {
	return filepath.Join(config.HomeDir(), "pending_login.json")
}

// SavePendingLogin persists the pending login state, stamping CreatedAt.
// Best-effort callers may ignore the error: losing the file only degrades
// attribution, never login itself.
func SavePendingLogin(p *PendingLogin) error {
	p.CreatedAt = time.Now().UnixMilli()
	path := pendingLoginPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// LoadPendingLogin returns the stored state when it matches challengeID and
// is less than 24h old, nil otherwise (absent, stale, or from another login).
func LoadPendingLogin(challengeID string) *PendingLogin {
	data, err := os.ReadFile(pendingLoginPath())
	if err != nil {
		return nil
	}
	var p PendingLogin
	if err := json.Unmarshal(data, &p); err != nil {
		return nil
	}
	if p.ChallengeID != challengeID {
		return nil
	}
	if time.Since(time.UnixMilli(p.CreatedAt)) > 24*time.Hour {
		return nil
	}
	return &p
}

// DeletePendingLogin removes the pending state; nil if it does not exist.
func DeletePendingLogin() error {
	err := os.Remove(pendingLoginPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
