package cred

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os/exec"
	"os/user"
	"strings"

	"quietport.app/quietport/internal/cryptobox"
)

// macOS: the vault key lives in the login Keychain as a generic password (service=serviceName, account=<user>).
// `security` is Apple's CLI over the same Keychain API; no admin rights, no cgo.

func account() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "quietport"
}

func loadOrCreate(appDir, service string) ([]byte, error) {
	out, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", service, "-a", account(), "-w").Output()
	if err == nil {
		k, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
		if derr == nil && len(k) == 32 {
			return k, nil
		}
	}
	k := cryptobox.RandomBytes(32)
	enc := base64.StdEncoding.EncodeToString(k)
	// -U updates if present; -T "" means no app is pre-trusted beyond the creator (security itself), which is what we want.
	cmd := exec.Command("/usr/bin/security", "add-generic-password", "-U", "-s", service, "-a", account(), "-w", enc, "-l", "Quietport", "-D", "Quietport vault key")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, errors.New("keychain write failed: " + strings.TrimSpace(stderr.String()))
	}
	return k, nil
}

func destroy(appDir, service string) error {
	_ = exec.Command("/usr/bin/security", "delete-generic-password", "-s", service, "-a", account()).Run()
	return nil
}
