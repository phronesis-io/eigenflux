package tradebff

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	delegationPrefix   = "efd1_"
	delegationDomain   = "eigenflux-console-delegation:v1"
	delegationIssuer   = "eigenflux-api"
	delegationAudience = "eigenflux-commission"
)

type DelegationRequest struct {
	AgentID        int64
	Scope          string
	Method         string
	Operation      string
	Body           []byte
	IdempotencyKey string
	BindMutation   bool
}

type delegationClaims struct {
	Version              int    `json:"ver"`
	Issuer               string `json:"iss"`
	Audience             string `json:"aud"`
	Subject              string `json:"sub"`
	Scope                string `json:"scope"`
	IssuedAt             int64  `json:"iat"`
	ExpiresAt            int64  `json:"exp"`
	TokenID              string `json:"jti"`
	Method               string `json:"method"`
	Operation            string `json:"operation"`
	BodySHA256           string `json:"body_sha256,omitempty"`
	IdempotencyKeySHA256 string `json:"idempotency_key_sha256,omitempty"`
}

type Delegator struct {
	kid        string
	privateKey ed25519.PrivateKey
	now        func() time.Time
	random     io.Reader
}

func NewDelegator(kid, encodedPrivateKey string) (*Delegator, error) {
	kid = strings.TrimSpace(kid)
	if !validDelegationKeyID(kid) {
		return nil, fmt.Errorf("invalid Commission delegation key id")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encodedPrivateKey))
	if err != nil {
		return nil, fmt.Errorf("decode Commission delegation private key")
	}
	var privateKey ed25519.PrivateKey
	switch len(decoded) {
	case ed25519.SeedSize:
		privateKey = ed25519.NewKeyFromSeed(decoded)
	case ed25519.PrivateKeySize:
		privateKey = ed25519.PrivateKey(decoded)
	default:
		return nil, fmt.Errorf("invalid Commission delegation private key length")
	}
	return &Delegator{kid: kid, privateKey: privateKey, now: time.Now, random: rand.Reader}, nil
}

func (d *Delegator) Token(request DelegationRequest) (string, error) {
	if d == nil || request.AgentID <= 0 || request.Scope == "" || request.Operation == "" || request.Method == "" {
		return "", fmt.Errorf("invalid Commission delegation request")
	}
	if request.BindMutation && strings.TrimSpace(request.IdempotencyKey) == "" {
		return "", fmt.Errorf("idempotency key is required")
	}
	var random [16]byte
	if _, err := io.ReadFull(d.random, random[:]); err != nil {
		return "", fmt.Errorf("create delegation token id: %w", err)
	}
	now := d.now().UTC()
	claims := delegationClaims{
		Version: 1, Issuer: delegationIssuer, Audience: delegationAudience,
		Subject: strconv.FormatInt(request.AgentID, 10), Scope: request.Scope,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(30 * time.Second).Unix(),
		TokenID: hex.EncodeToString(random[:]), Method: strings.ToUpper(request.Method), Operation: request.Operation,
	}
	if request.BindMutation {
		claims.BodySHA256 = delegationDigest(request.Body)
		claims.IdempotencyKeySHA256 = delegationDigest([]byte(strings.TrimSpace(request.IdempotencyKey)))
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal delegation claims: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := ed25519.Sign(d.privateKey, delegationTranscript(d.kid, encoded))
	return delegationPrefix + d.kid + "." + encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func delegationTranscript(kid, payload string) []byte {
	return []byte(delegationDomain + "\n" + kid + "\n" + payload)
}

func delegationDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func validDelegationKeyID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}
