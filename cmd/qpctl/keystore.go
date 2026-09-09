package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"quietport.app/quietport/internal/cred"
	"quietport.app/quietport/internal/model"
)

// Keystore holds every circle key the operator has ever generated, sealed with the OS credential store
// (Keychain / DPAPI). This is the only place plaintext circle keys exist besides member devices (SRD §8 note).
type Keystore struct {
	cred       *credStore
	Keys       map[string][]model.CircleKey `json:"keys"` // slug -> generations, ascending
	LastVerify time.Time                    `json:"last_verify"`
	LastExport time.Time                    `json:"last_export"`
}

type credStore struct{ s *cred.Store }

func (c *credStore) SealJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return c.s.Seal(b)
}
func (c *credStore) OpenJSON(sealed string, v any) error {
	b, err := c.s.Open(sealed)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func keystorePath() string { return filepath.Join(confDir(), "keystore.sealed") }

func openKeystore() (*Keystore, error) {
	_ = os.MkdirAll(confDir(), 0o700)
	s, err := cred.Open(confDir(), "Quietport Operator")
	if err != nil {
		return nil, err
	}
	ks := &Keystore{cred: &credStore{s}, Keys: map[string][]model.CircleKey{}}
	if b, err := os.ReadFile(keystorePath()); err == nil {
		if err := ks.cred.OpenJSON(string(b), ks); err != nil {
			return nil, errors.New("keystore cannot be opened with this account's credential store")
		}
		if ks.Keys == nil {
			ks.Keys = map[string][]model.CircleKey{}
		}
	}
	return ks, nil
}

func (k *Keystore) save() error {
	sealed, err := k.cred.SealJSON(k)
	if err != nil {
		return err
	}
	tmp := keystorePath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(sealed), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, keystorePath())
}

func (k *Keystore) Current(slug string) (model.CircleKey, bool) {
	ks := k.Keys[slug]
	if len(ks) == 0 {
		return model.CircleKey{}, false
	}
	return ks[len(ks)-1], true
}

func (k *Keystore) Gen(slug string, gen int) (model.CircleKey, bool) {
	for _, x := range k.Keys[slug] {
		if x.Generation == gen {
			return x, true
		}
	}
	return model.CircleKey{}, false
}

func (k *Keystore) Add(key model.CircleKey) {
	k.Keys[key.Slug] = append(k.Keys[key.Slug], key)
	sort.Slice(k.Keys[key.Slug], func(i, j int) bool { return k.Keys[key.Slug][i].Generation < k.Keys[key.Slug][j].Generation })
}

// Export is the plaintext form used by keys export/split (encrypted before it touches disk).
type Export struct {
	ExportedAt time.Time                    `json:"exported_at"`
	Keys       map[string][]model.CircleKey `json:"keys"`
}
