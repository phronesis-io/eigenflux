// Package integrationidentity implements credentials for managed integrations.
// These credentials are separate from personal Agent sessions and onboarding.
package integrationidentity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

// NewCredential returns a secret once and its database verification value.
// No shared SDK secret is embedded in a client or published artifact.
func NewCredential() (secret, digest string, err error) {
	var entropy [32]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return "", "", err
	}
	secret = "efi_" + base64.RawURLEncoding.EncodeToString(entropy[:])
	digest, err = CredentialHash(secret)
	return
}

// CredentialHash rejects malformed credentials before any database lookup.
func CredentialHash(secret string) (string, error) {
	if !strings.HasPrefix(secret, "efi_") || len(secret) != 47 {
		return "", errors.New("invalid integration credential")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(secret[4:])
	if err != nil || len(decoded) != 32 {
		return "", errors.New("invalid integration credential")
	}
	digest := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(digest[:]), nil
}
