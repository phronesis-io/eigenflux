package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/config"
)

type IdentityKey struct {
	KeyType    string `json:"key_type"`
	PrivateKey string `json:"private_key"`
	CreatedAt  int64  `json:"created_at"`
}

func identityPath(serverName string) string {
	return filepath.Join(config.HomeDir(), "servers", serverName, "identity.json")
}

func LoadOrCreateIdentity(serverName string) (ed25519.PublicKey, ed25519.PrivateKey, bool, error) {
	path := identityPath(serverName)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, false, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, nil, false, fmt.Errorf("secure identity directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, nil, false, fmt.Errorf("identity path must be a regular file, not a symlink")
		}
		if info.Mode().Perm()&0077 != 0 {
			return nil, nil, false, fmt.Errorf("identity file permissions are too broad; require 0600")
		}
		publicKey, privateKey, loadErr := loadIdentity(path)
		return publicKey, privateKey, false, loadErr
	} else if !os.IsNotExist(err) {
		return nil, nil, false, err
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, false, err
	}
	data, err := json.MarshalIndent(IdentityKey{
		KeyType: "ed25519-v1", PrivateKey: base64.RawURLEncoding.EncodeToString(privateKey),
		CreatedAt: time.Now().UnixMilli(),
	}, "", "  ")
	if err != nil {
		return nil, nil, false, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return LoadOrCreateIdentity(serverName)
		}
		return nil, nil, false, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, nil, false, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, nil, false, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, nil, false, err
	}
	return publicKey, privateKey, true, nil
}

func loadIdentity(path string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var stored IdentityKey
	if json.Unmarshal(data, &stored) != nil || stored.KeyType != "ed25519-v1" {
		return nil, nil, fmt.Errorf("invalid identity file")
	}
	raw, err := base64.RawURLEncoding.DecodeString(stored.PrivateKey)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, nil, fmt.Errorf("invalid Ed25519 private key")
	}
	privateKey := ed25519.PrivateKey(raw)
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return nil, nil, fmt.Errorf("invalid Ed25519 public key")
	}
	return publicKey, privateKey, nil
}

func IdentityFingerprint(publicKey ed25519.PublicKey) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("ed25519-v1\x00"))
	_, _ = hash.Write(publicKey)
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}
