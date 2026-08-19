package commissionapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func FileDigest(path string) (size int64, digest string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, "", err
	}
	if !info.Mode().IsRegular() {
		return 0, "", fmt.Errorf("%s is not a regular file", path)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return 0, "", err
	}
	return info.Size(), hex.EncodeToString(hash.Sum(nil)), nil
}

func Upload(ctx context.Context, httpClient *http.Client, grant TransferGrant, localPath string) error {
	ctx, cancel, err := grantContext(ctx, grant)
	if err != nil {
		return err
	}
	defer cancel()
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()
	method := strings.ToUpper(strings.TrimSpace(grant.Method))
	if method == "" {
		method = http.MethodPut
	}
	request, err := http.NewRequestWithContext(ctx, method, grant.URL, file)
	if err != nil {
		return err
	}
	for key, value := range grant.Headers {
		request.Header.Set(key, value)
	}
	response, err := transferClient(httpClient).Do(request)
	if err != nil {
		return fmt.Errorf("workspace upload failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("workspace upload failed (HTTP %d): %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func Download(ctx context.Context, httpClient *http.Client, grant TransferGrant, destination string, force bool) error {
	ctx, cancel, err := grantContext(ctx, grant)
	if err != nil {
		return err
	}
	defer cancel()
	if !force {
		if _, err := os.Stat(destination); err == nil {
			return fmt.Errorf("destination already exists: %s (use --force to replace)", destination)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	method := strings.ToUpper(strings.TrimSpace(grant.Method))
	if method == "" {
		method = http.MethodGet
	}
	request, err := http.NewRequestWithContext(ctx, method, grant.URL, nil)
	if err != nil {
		return err
	}
	for key, value := range grant.Headers {
		request.Header.Set(key, value)
	}
	response, err := transferClient(httpClient).Do(request)
	if err != nil {
		return fmt.Errorf("workspace download failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("workspace download failed (HTTP %d): %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".eigenflux-download-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := io.Copy(temp, response.Body); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceFile(tempPath, destination, force); err != nil {
		return err
	}
	committed = true
	return nil
}

func grantContext(parent context.Context, grant TransferGrant) (context.Context, context.CancelFunc, error) {
	if grant.ExpiresAt <= 0 {
		ctx, cancel := context.WithCancel(parent)
		return ctx, cancel, nil
	}
	deadline := time.UnixMilli(grant.ExpiresAt)
	if !deadline.After(time.Now()) {
		return nil, nil, fmt.Errorf("transfer grant has expired")
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	return ctx, cancel, nil
}

func transferClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	clone := *base
	// API calls are short; workspace transfers are bounded by grant expiry and
	// may legitimately take longer than the API client's 30-second timeout.
	clone.Timeout = 0
	return &clone
}

func replaceFile(tempPath, destination string, force bool) error {
	if !force {
		return os.Rename(tempPath, destination)
	}
	// Unix rename atomically replaces the destination. Platforms that reject
	// replacement fall back to a same-directory backup and restore on failure.
	if err := os.Rename(tempPath, destination); err == nil {
		return nil
	}
	if _, err := os.Stat(destination); err != nil {
		return err
	}
	backup, err := os.CreateTemp(filepath.Dir(destination), ".eigenflux-replaced-*")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	if err := os.Rename(destination, backupPath); err != nil {
		return err
	}
	if err := os.Rename(tempPath, destination); err != nil {
		_ = os.Rename(backupPath, destination)
		return err
	}
	return os.Remove(backupPath)
}
