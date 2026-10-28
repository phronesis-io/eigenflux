package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/selfupdate"
)

func verifyPublished(cdn, minimum string, key ed25519.PublicKey) error {
	if !selfupdate.ValidVersion(minimum) {
		return fmt.Errorf("invalid minimum CLI version %q", minimum)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	base := strings.TrimRight(cdn, "/") + "/cli/"
	get := func(path string) (*http.Response, error) {
		resp, err := client.Get(base + path + "?verify=" + fmt.Sprint(time.Now().UnixNano()))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
		}
		return resp, nil
	}
	resp, err := get("latest/release.json")
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		return err
	}
	m, err := selfupdate.Verify(data, key)
	if err != nil {
		return err
	}
	if selfupdate.Compare(m.Version, minimum) < 0 {
		return fmt.Errorf("published CLI %s is below Skills minimum %s", m.Version, minimum)
	}
	// Existing releases remain verifiable before their first complete-package
	// publication. A present package pointer must authenticate and agree with
	// the independently signed legacy pointer; other HTTP failures are fatal.
	packageResponse, err := client.Get(base + "latest/package-release.json?verify=" + fmt.Sprint(time.Now().UnixNano()))
	if err != nil {
		return err
	}
	if packageResponse.StatusCode == http.StatusOK {
		packageData, readErr := io.ReadAll(io.LimitReader(packageResponse.Body, 1<<20))
		packageResponse.Body.Close()
		if readErr != nil {
			return readErr
		}
		packaged, verifyErr := selfupdate.Verify(packageData, key)
		if verifyErr != nil {
			return verifyErr
		}
		if packaged.Version != m.Version || !reflect.DeepEqual(packaged.Artifacts, m.Artifacts) || len(packaged.Packages) != 6 {
			return fmt.Errorf("complete and legacy CLI releases disagree")
		}
		m = packaged
	} else {
		packageResponse.Body.Close()
		if packageResponse.StatusCode != http.StatusNotFound {
			return fmt.Errorf("package release: HTTP %d", packageResponse.StatusCode)
		}
	}
	artifacts := map[string]selfupdate.Artifact{}
	for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64.exe", "windows-arm64.exe"} {
		name := "eigenflux-" + platform
		artifact, ok := m.Artifacts[name]
		if !ok {
			return fmt.Errorf("missing artifact %s", name)
		}
		artifacts[name] = artifact
		if m.Packages != nil {
			archive, ok := m.Packages[name+".zip"]
			if !ok {
				return fmt.Errorf("missing complete package %s", name)
			}
			artifacts[name+".zip"] = archive
		}
	}
	for name, a := range artifacts {
		if a.Size <= 0 || a.Size > 256<<20 || len(a.SHA256) != 64 {
			return fmt.Errorf("invalid artifact %s", name)
		}
		resp, err := get(m.Version + "/" + name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, err := io.Copy(hash, io.LimitReader(resp.Body, a.Size+1))
		resp.Body.Close()
		if err != nil {
			return err
		}
		if n != a.Size || hex.EncodeToString(hash.Sum(nil)) != a.SHA256 {
			return fmt.Errorf("published artifact does not match signed release: %s", name)
		}
	}

	return nil
}
