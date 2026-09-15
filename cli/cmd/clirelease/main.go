// clirelease signs the exact platform artifacts uploaded by the CLI publisher.
package main

import (
	"cli.eigenflux.ai/internal/selfupdate"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	dir := flag.String("dir", "", "built CLI artifact directory")
	version := flag.String("version", "", "stable release version")
	keyFile := flag.String("signing-key-file", "", "Ed25519 private key file")
	cdn := flag.String("verify-cdn", "", "verify the published CLI and all platform artifacts")
	minimum := flag.String("min-version", "", "minimum compatible CLI version")
	flag.Parse()
	if *cdn != "" {
		key, err := base64.StdEncoding.DecodeString(os.Getenv("EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY"))
		if err != nil {
			log.Fatal(err)
		}
		if err := verifyPublished(*cdn, *minimum, key); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *dir == "" || !selfupdate.ValidVersion(*version) || *keyFile == "" {
		log.Fatal("--dir, --version and --signing-key-file are required")
	}
	key, err := loadKey(*keyFile)
	if err != nil {
		log.Fatal(err)
	}
	m := selfupdate.Manifest{Version: *version, Artifacts: map[string]selfupdate.Artifact{}}
	for _, osName := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			name := "eigenflux-" + osName + "-" + arch
			if osName == "windows" {
				name += ".exe"
			}
			b, err := os.ReadFile(filepath.Join(*dir, name))
			if err != nil {
				log.Fatal(err)
			}
			if len(b) == 0 {
				log.Fatal("empty binary: ", name)
			}
			sum := sha256.Sum256(b)
			m.Artifacts[name] = selfupdate.Artifact{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b))}
		}
	}
	if err := selfupdate.Sign(&m, key); err != nil {
		log.Fatal(err)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if _, err = selfupdate.Verify(b, key.Public().(ed25519.PublicKey)); err != nil {
		log.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(*dir, "release.json"), b, 0644); err != nil {
		log.Fatal(err)
	}
}

func loadKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if block, _ := pem.Decode(b); block != nil {
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		if k, ok := key.(ed25519.PrivateKey); ok {
			return k, nil
		}
		return nil, fmt.Errorf("key is not Ed25519")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, err
	}
	if len(raw) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(raw), nil
	}
	if len(raw) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(raw), nil
	}
	return nil, fmt.Errorf("invalid Ed25519 key size")
}
