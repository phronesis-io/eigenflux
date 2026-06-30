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
	"encoding/json"
	"flag"
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
	flag.Parse()

	// Single source of truth for the production allowlist: build.sh/install.sh
	// derive their list from this so it never drifts from the Go constant.
	if *printAllowlist {
		for _, name := range skills.ProdAllowlist {
			os.Stdout.WriteString(name + "\n")
		}
		return
	}

	if *src == "" || *ver == "" {
		log.Fatal("manifestgen: --skills-dir and --cli-version are required")
	}

	m, err := skills.GenerateManifest(*src, *ver, skills.ProdAllowlist, time.Now().Unix())
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
