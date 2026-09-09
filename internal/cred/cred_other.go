//go:build !darwin && !windows

package cred

import (
	"os"

	"quietport.app/quietport/internal/cryptobox"
)

// Linux (best effort, C-5): a 0600 file in the user's config dir. No OS keyring dependency to keep the agent a single static binary.

func loadOrCreate(appDir, service string) ([]byte, error) {
	p := fileKeyPath(appDir)
	if k, err := readFile0600(p); err == nil && len(k) == 32 {
		return k, nil
	}
	k := cryptobox.RandomBytes(32)
	if err := os.WriteFile(p, k, 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

func destroy(appDir, service string) error { return os.Remove(fileKeyPath(appDir)) }
