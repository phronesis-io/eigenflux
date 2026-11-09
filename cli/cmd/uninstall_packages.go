package cmd

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/selfupdate"
	"cli.eigenflux.ai/internal/skills"
)

func planUninstallPackages(ctx context.Context, bin string) (*selfupdate.InstallationCleanup, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	key, _ := base64.StdEncoding.DecodeString(skills.VerifyPublicKeyBase64)
	return selfupdate.PlanInstallationCleanup(ctx, bin, key)
}

func (o *uninstallObservation) verifyPackageCleanup(ctx context.Context) error {
	_, err := o.livePackageCleanup(ctx)
	return err
}

func (o *uninstallObservation) livePackageCleanup(ctx context.Context) (*selfupdate.InstallationCleanup, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	key, _ := base64.StdEncoding.DecodeString(skills.VerifyPublicKeyBase64)
	return selfupdate.RevalidateInstallationCleanup(ctx, o.Executable, key, o.PackageCleanup)
}

func (o *uninstallObservation) removePackages() error {
	if o.PackageCleanup == nil {
		return nil
	}
	live, err := o.livePackageCleanup(context.Background())
	if err != nil {
		return err
	}
	pointer := o.Executable + ".current.json"
	remove := func(path string) error {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		o.Mutated = true
		return nil
	}
	nestedRemoved := false
	for _, path := range live.RemovalFiles(o.Executable) {
		if path == pointer {
			continue
		}
		// Empty payload directories precede the retained ZIP, so a retry
		// without the ZIP never has to trust receipt-supplied subdirectories.
		if !nestedRemoved && isPackageMetadata(o.Executable, path) {
			for _, directory := range live.Directories {
				if directory == o.Executable+".versions" || filepath.Dir(directory) == o.Executable+".versions" {
					continue
				}
				if err := remove(directory); err != nil {
					return err
				}
			}
			nestedRemoved = true
		}
		if err := remove(path); err != nil {
			return err
		}
	}
	for _, path := range live.Directories {
		// Never recursively delete a directory: unexpected additions survive.
		if err := remove(path); err != nil {
			return err
		}
	}
	if _, ok := live.Files[pointer]; ok {
		if err := remove(pointer); err != nil {
			return err
		}
	}
	return nil
}

func isPackageMetadata(root, file string) bool {
	if filepath.Dir(filepath.Dir(file)) != root+".versions" {
		return false
	}
	switch filepath.Base(file) {
	case "release.json", ".package-release.json", ".package.zip":
		return true
	}
	return false
}
