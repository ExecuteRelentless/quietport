// Package cred protects secrets at rest with the OS credential store (FR-61 / NFR-22):
// DPAPI (current-user scope) on Windows, the login Keychain on macOS, and a 0600 file key on Linux (best effort, C-5).
//
// The pattern is the same everywhere: one 32-byte "vault key" per install is protected by the OS store, and
// config secrets are encrypted with it, so config.json stays a single file (FR-60) with ciphertext fields.
package cred

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"

	"quietport.app/quietport/internal/cryptobox"
)

// Store is an opened vault key.
type Store struct {
	key []byte
}

// Open loads (or creates) the vault key for the app directory. serviceName scopes the OS entry.
func Open(appDir, serviceName string) (*Store, error) {
	k, err := loadOrCreate(appDir, serviceName)
	if err != nil {
		return nil, err
	}
	return &Store{key: k}, nil
}

// Seal returns base64 ciphertext for plaintext.
func (s *Store) Seal(pt []byte) (string, error) {
	ct, err := cryptobox.EncryptWithKey(s.key, pt)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// Open decrypts a Seal() string.
func (s *Store) Open(ct string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		return nil, err
	}
	return cryptobox.DecryptWithKey(s.key, b)
}

// Destroy removes the OS entry (uninstall).
func Destroy(appDir, serviceName string) error { return destroy(appDir, serviceName) }

// fileKeyPath is used by the linux fallback and as the DPAPI blob location on windows.
func fileKeyPath(appDir string) string { return filepath.Join(appDir, "vault.key") }

func readFile0600(p string) ([]byte, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		_ = os.Chmod(p, 0o600)
	}
	return os.ReadFile(p)
}

var errNoKey = errors.New("no vault key")
