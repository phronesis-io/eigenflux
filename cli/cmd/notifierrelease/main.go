// notifierrelease authenticates immutable, fully validated native helper bundles.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

type artifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type receipt struct {
	Schema     int                 `json:"schema"`
	Version    string              `json:"version"`
	Source     string              `json:"source"`
	TeamID     string              `json:"team_id"`
	Validation string              `json:"validation"`
	Artifacts  map[string]artifact `json:"artifacts"`
	Signature  string              `json:"signature,omitempty"`
}

var semver = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var team = regexp.MustCompile(`^[A-Z0-9]{10}$`)

func payload(r receipt) []byte {
	r.Signature = ""
	b, _ := json.Marshal(r)
	return append([]byte("eigenflux_notifier_release.v1\n"), b...)
}
func validate(r receipt) error {
	if r.Schema != 1 || !semver.MatchString(r.Version) || !team.MatchString(r.TeamID) || r.Validation != "codesign_stapler_gatekeeper" || len(r.Artifacts) != 2 {
		return errors.New("invalid notifier receipt")
	}
	if b, e := hex.DecodeString(r.Source); e != nil || len(b) != 32 || strings.ToLower(r.Source) != r.Source {
		return errors.New("invalid source digest")
	}
	for _, a := range []string{"amd64", "arm64"} {
		v, ok := r.Artifacts[a]
		b, e := hex.DecodeString(v.SHA256)
		if !ok || e != nil || len(b) != 32 || strings.ToLower(v.SHA256) != v.SHA256 || v.Size <= 0 || v.Size > 64<<20 {
			return errors.New("invalid notifier artifact")
		}
	}
	return nil
}
func private(path string) (ed25519.PrivateKey, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	if block, _ := pem.Decode(b); block != nil {
		k, e := x509.ParsePKCS8PrivateKey(block.Bytes)
		if e != nil {
			return nil, e
		}
		if key, ok := k.(ed25519.PrivateKey); ok {
			return key, nil
		}
		return nil, errors.New("key is not Ed25519")
	}
	raw, e := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if e != nil {
		return nil, e
	}
	if len(raw) == 32 {
		return ed25519.NewKeyFromSeed(raw), nil
	}
	if len(raw) == 64 {
		return ed25519.PrivateKey(raw), nil
	}
	return nil, errors.New("invalid private key")
}
func run() error {
	mode := flag.String("mode", "verify", "sign or verify")
	file := flag.String("receipt", "", "receipt JSON")
	keyfile := flag.String("key-file", "", "signing key file")
	flag.Parse()
	b, e := os.ReadFile(*file)
	if e != nil {
		return e
	}
	if len(b) > 16384 {
		return errors.New("receipt too large")
	}
	var r receipt
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&r); e != nil {
		return e
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing receipt data")
	}
	if e = validate(r); e != nil {
		return e
	}
	pub, e := base64.StdEncoding.DecodeString(os.Getenv("EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY"))
	if e != nil || len(pub) != 32 {
		return errors.New("trusted public key required")
	}
	switch *mode {
	case "sign":
		k, e := private(*keyfile)
		if e != nil {
			return e
		}
		if !bytes.Equal(k.Public().(ed25519.PublicKey), pub) {
			return errors.New("signing key mismatch")
		}
		r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(k, payload(r)))
	case "verify":
		sig, e := base64.StdEncoding.DecodeString(r.Signature)
		if e != nil || !ed25519.Verify(pub, payload(r), sig) {
			return errors.New("untrusted notifier receipt")
		}
	default:
		return errors.New("invalid mode")
	}
	out, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	_, e = os.Stdout.Write(append(out, '\n'))
	return e
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
