package desktopnotify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

func recordBundleOwner(app, digest string) error {
	path := filepath.Join(filepath.Dir(app), ".bundle-owner")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("notification ownership marker must be regular")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(app), ".owner-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write([]byte(digest))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func platformUninstall(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	directory := filepath.Join(home, "Library", "Application Support", "EigenFlux", "Notifications")
	app := filepath.Join(directory, appName)
	if _, err := os.Lstat(app); os.IsNotExist(err) {
		return nil
	}
	release, err := lockHelper(ctx, directory, true)
	if err != nil {
		return err
	}
	defer release()
	ownerPath := filepath.Join(directory, ".bundle-owner")
	info, err := os.Lstat(ownerPath)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("notification helper ownership is unknown; preserve it")
	}
	owner, err := os.ReadFile(ownerPath)
	if err != nil {
		return err
	}
	actual, err := bundleDigest(app)
	if err != nil || string(owner) != actual {
		return errors.New("notification helper changed; preserve it")
	}
	if err := stopHelper(ctx, app); err != nil {
		return err
	}
	if err := os.RemoveAll(app); err != nil {
		return err
	}
	return os.Remove(ownerPath)
}
