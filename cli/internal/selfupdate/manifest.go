package selfupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const manifestDomain = "eigenflux_cli_release.v1\n"

type Artifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	Version   string              `json:"version"`
	Artifacts map[string]Artifact `json:"artifacts"`
	Signature string              `json:"signature,omitempty"`
}

var stableVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func ValidVersion(v string) bool { return stableVersion.MatchString(v) }

func Compare(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		// Compare decimal strings without integer overflow.
		if len(pa[i]) < len(pb[i]) {
			return -1
		}
		if len(pa[i]) > len(pb[i]) {
			return 1
		}
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return 0
}

func payload(m Manifest) []byte {
	m.Signature = ""
	b, _ := json.Marshal(m)
	return append([]byte(manifestDomain), b...)
}

func Sign(m *Manifest, key ed25519.PrivateKey) error {
	if !ValidVersion(m.Version) || len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid release version or signing key")
	}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload(*m)))
	return nil
}

func Verify(data []byte, key ed25519.PublicKey) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, payload(m), sig) {
		return nil, fmt.Errorf("untrusted CLI release signature")
	}
	if !ValidVersion(m.Version) {
		return nil, fmt.Errorf("invalid CLI release version %s", strconv.Quote(m.Version))
	}
	return &m, nil
}
