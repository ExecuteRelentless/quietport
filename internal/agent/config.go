package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"quietport.app/quietport/internal/cred"
	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/model"
)

// Config is the single client config file (FR-60). Every secret field is sealed with the OS-protected vault key (FR-61).
type Config struct {
	Version        int    `json:"version"`
	AgentVersion   string `json:"agent_version"`
	DeviceID       int64  `json:"device_id"`
	HubAPI         string `json:"hub_api"`
	LoginServer    string `json:"login_server"`
	SupportContact string `json:"support_contact"`
	OperatorName   string `json:"operator_name"`
	SocksPort      int    `json:"socks_port"`
	UIPort         int    `json:"ui_port"`  // loopback port of the "Share a folder" page
	UIToken        string `json:"ui_token"` // per-install token carried in the shortcut URL
	SyncInterval   int    `json:"sync_interval_seconds"`
	S3Endpoint     string `json:"s3_endpoint"`

	DeviceTokenSealed string `json:"device_token"`          // sealed
	DeviceKeysSealed  string `json:"device_keys"`           // sealed JSON cryptobox.DeviceKeys
	PreAuthSealed     string `json:"preauth_key,omitempty"` // sealed, only until first successful login

	Circles []CircleState `json:"circles"`
}

type CircleState struct {
	model.CircleConfig
	KeySealed  string   `json:"key"` // sealed JSON model.CircleKey for CircleConfig.Generation
	KeyGen     int      `json:"key_generation"`
	NeedsKey   bool     `json:"needs_key"` // no key for the current generation: re-provisioning needed (FR-57)
	Resync     bool     `json:"resync"`    // next run must be --resync
	FilterHash string   `json:"filter_hash"`
	Removed    bool     `json:"removed"`                  // no longer a member; folder left in place, sync stopped
	Excluded   []string `json:"excluded_paths,omitempty"` // FR-40
	S3Sealed   string   `json:"s3"`                       // sealed "access:secret"
}

type Store struct {
	mu   sync.Mutex
	path string
	cred *cred.Store
	cfg  Config
}

func OpenStore() (*Store, error) {
	if err := os.MkdirAll(AppDir(), 0o700); err != nil {
		return nil, err
	}
	c, err := cred.Open(AppDir(), ServiceName)
	if err != nil {
		return nil, err
	}
	s := &Store{path: ConfigPath(), cred: c}
	b, err := os.ReadFile(s.path)
	if err == nil {
		if err := json.Unmarshal(b, &s.cfg); err != nil {
			return nil, errors.New("config.json is unreadable: " + err.Error())
		}
	}
	return s, nil
}

func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

func (s *Store) Update(fn func(c *Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.cfg)
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil { // atomic (NFR-14)
		return err
	}
	_ = os.Chmod(s.path, 0o600) // FR-62
	return nil
}

func (s *Store) Seal(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return s.cred.Seal(b)
}
func (s *Store) Open(sealed string, v any) error {
	if sealed == "" {
		return errors.New("empty")
	}
	b, err := s.cred.Open(sealed)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (s *Store) DeviceToken() string {
	var t string
	_ = s.Open(s.Config().DeviceTokenSealed, &t)
	return t
}
func (s *Store) DeviceKeys() (cryptobox.DeviceKeys, error) {
	var k cryptobox.DeviceKeys
	err := s.Open(s.Config().DeviceKeysSealed, &k)
	return k, err
}
func (s *Store) CircleKey(cs CircleState) (model.CircleKey, error) {
	var k model.CircleKey
	err := s.Open(cs.KeySealed, &k)
	return k, err
}
func (s *Store) CircleS3(cs CircleState) (string, string) {
	var v [2]string
	_ = s.Open(cs.S3Sealed, &v)
	return v[0], v[1]
}

// State is runtime status persisted for `qp status` and heartbeats.
type State struct {
	LastHeartbeat   time.Time                     `json:"last_heartbeat"`
	LastHeartbeatOK bool                          `json:"last_heartbeat_ok"`
	ConnectionType  string                        `json:"connection_type"`
	TailnetIP       string                        `json:"tailnet_ip"`
	Circles         map[string]model.CircleHealth `json:"circles"`
	ErrorCount      int                           `json:"error_count"`
	Conditions      []string                      `json:"conditions"`
	Paused          string                        `json:"paused,omitempty"`
	LastSyncOK      time.Time                     `json:"last_sync_ok"`
	Notified        map[string]time.Time          `json:"notified"`
	StartedAt       time.Time                     `json:"started_at"`
	PendingBytes    int64                         `json:"pending_bytes"`
}

func LoadState() State {
	var st State
	b, err := os.ReadFile(StatePath())
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Circles == nil {
		st.Circles = map[string]model.CircleHealth{}
	}
	if st.Notified == nil {
		st.Notified = map[string]time.Time{}
	}
	return st
}

func SaveState(st State) {
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := StatePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, StatePath())
	}
}

func CircleDir(displayName string) string { return filepath.Join(SyncRoot(), safeName(displayName)) }

// safeName is the directory a folder's name becomes under the sync root (docs/adr/0020).
func safeName(s string) string {
	if name := model.FolderName(s); name != "" {
		return name
	}
	return "Shared"
}
