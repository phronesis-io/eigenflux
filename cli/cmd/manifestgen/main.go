// Command manifestgen builds the skills manifest.json consumed by
// `eigenflux skills sync`. It is invoked by cli/scripts/build.sh against the
// staged production-skill tree and is NOT part of the eigenflux subcommand tree.
//
// Usage:
//
//	go run ./cmd/manifestgen --skills-dir <stage> --cli-version 0.0.16 \
//	    --tarball <build>/skills.tar.gz --out <build>/manifest.json
package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"cli.eigenflux.ai/internal/skills"
)

func main() {
	src := flag.String("skills-dir", "", "staged skills directory (allowlisted production skills)")
	ver := flag.String("cli-version", "", "CLI version to stamp into the manifest")
	tarball := flag.String("tarball", "", "optional skills.tar.gz to record tar_sha256")
	out := flag.String("out", "", "output manifest.json path (stdout if empty)")
	printAllowlist := flag.Bool("print-allowlist", false, "print the production skill allowlist (one per line) and exit")
	printPublicKey := flag.Bool("print-public-key", false, "print the signing key's Ed25519 public key as base64 and exit")
	minCLI := flag.String("min-cli-version", "", "minimum CLI version that may adopt this bundle (compatibility floor)")
	sequence := flag.Uint64("sequence", 0, "monotonic Skills release sequence")
	signingKeyFile := flag.String("signing-key-file", "", "Ed25519 private key file (PKCS8 PEM or base64 seed/private key)")
	flag.Parse()

	// Discover the production set from the source tree so future official ef-*
	// Skills are included without another hard-coded list change.
	if *printAllowlist {
		if *src == "" {
			log.Fatal("manifestgen: --skills-dir is required with --print-allowlist")
		}
		names, err := skills.DiscoverProductionSkills(*src)
		if err != nil {
			log.Fatalf("manifestgen: %v", err)
		}
		for _, name := range names {
			os.Stdout.WriteString(name + "\n")
		}
		return
	}
	if *printPublicKey {
		if *signingKeyFile == "" {
			log.Fatal("manifestgen: --signing-key-file is required with --print-public-key")
		}
		privateKey, err := loadSigningKey(*signingKeyFile)
		if err != nil {
			log.Fatalf("manifestgen: signing key: %v", err)
		}
		_, _ = os.Stdout.WriteString(base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)) + "\n")
		return
	}

	if *src == "" || *ver == "" || *sequence == 0 || *signingKeyFile == "" {
		log.Fatal("manifestgen: --skills-dir, --cli-version, --sequence and --signing-key-file are required")
	}

	m, err := skills.GenerateManifest(*src, *ver, *minCLI, nil, time.Now().Unix())
	if err != nil {
		log.Fatalf("manifestgen: %v", err)
	}
	if *tarball != "" {
		sum, err := skills.TarballSHA256(*tarball)
		if err != nil {
			log.Fatalf("manifestgen: tarball sha: %v", err)
		}
		m.TarSHA256 = sum
	}
	m.Sequence = *sequence
	privateKey, err := loadSigningKey(*signingKeyFile)
	if err != nil {
		log.Fatalf("manifestgen: signing key: %v", err)
	}
	if err := skills.SignManifest(m, privateKey); err != nil {
		log.Fatalf("manifestgen: sign: %v", err)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		log.Fatalf("manifestgen: marshal: %v", err)
	}
	data = append(data, '\n')
	if *out == "" {
		os.Stdout.Write(data)
		return
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		log.Fatalf("manifestgen: write: %v", err)
	}
}

func loadSigningKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if block, _ := pem.Decode(data); block != nil {
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		privateKey, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS8 key is not Ed25519")
		}
		return privateKey, nil
	}
	raw, err := base64.StdEncoding.DecodeString(string(bytesTrimSpace(data)))
	if err != nil {
		return nil, fmt.Errorf("decode base64 key: %w", err)
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	default:
		return nil, fmt.Errorf("key must be a %d-byte seed or %d-byte private key", ed25519.SeedSize, ed25519.PrivateKeySize)
	}
}

func bytesTrimSpace(data []byte) []byte {
	start, end := 0, len(data)
	for start < end && (data[start] == ' ' || data[start] == '\n' || data[start] == '\r' || data[start] == '\t') {
		start++
	}
	for end > start && (data[end-1] == ' ' || data[end-1] == '\n' || data[end-1] == '\r' || data[end-1] == '\t') {
		end--
	}
	return data[start:end]
}
