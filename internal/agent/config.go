package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// Folder is this circle's directory under the sync root on this computer, chosen when the circle arrived and kept
	// after that (docs/adr/0021). Empty in a config written before 0.1.26, where the display name was the directory.
	Folder string `json:"folder,omitempty"`
	// Refused: rclone's guard refused this folder's bisync refusalsBeforeReport times in a row, so it takes no full sync
	// a person did not choose (docs/adr/0023). Cleared by the next sync that succeeds.
	Refused bool `json:"refused,omitempty"`
	// ResyncKeep: whose copies the pending full sync keeps where the sides differ, when a person asked for it
	// (docs/adr/0023): "this", "hub" or "newer". Empty means this device's, as every scheduled full sync does.
	ResyncKeep string `json:"resync_keep,omitempty"`
	// AsidePending: the directory is new to this circle and has not been emptied yet, so the circle does not sync
	// (docs/adr/0021). Set when a set-aside fails, cleared by the one that succeeds.
	AsidePending bool `json:"aside_pending,omitempty"`
}

// resyncMode: how the circle's next cycle runs, as a normal bisync or as a full sync keeping someone's copies. While
// the folder is refused only a person's choice makes it a full sync (docs/adr/0023).
func (cs CircleState) resyncMode() string {
	if !cs.Resync || (cs.Refused && cs.ResyncKeep == "") {
		return noResync
	}
	switch cs.ResyncKeep {
	case "hub":
		return resyncKeepHub
	case "newer":
		return resyncKeepNewer
	}
	return resyncThisDevice
}

// requestFullSync asks for one full sync of a folder on this device, keeping this device's copies ("this"), the
// hub's ("hub") or the newer of each ("newer") where the sides differ (docs/adr/0023). The folder is named as the
// member sees it in the sync root, without regard to case, or by the circle's slug. It reports the folder's name and
// the circle's slug. A folder that only receives or only sends here never bisyncs, and one waiting for its key cannot
// sync, so neither is accepted.
func (c *Config) requestFullSync(folder, keep string) (name, slug string, err error) {
	if _, ok := keepWords[keep]; !ok {
		return "", "", errors.New("say whose copies to keep where the two sides differ: this, hub or newer")
	}
	for i := range c.Circles {
		cs := &c.Circles[i]
		if cs.Removed || (!strings.EqualFold(cs.folder(), folder) && cs.Slug != folder) {
			continue
		}
		switch {
		case cs.NeedsKey:
			return "", "", errors.New(cs.folder() + " is waiting for a new invitation on this computer, so it cannot sync yet")
		case syncModeOf(*cs) != "bisync":
			return "", "", errors.New(cs.folder() + " only receives or only sends on this computer, so it never takes a full sync")
		}
		cs.Resync, cs.ResyncKeep = true, keep
		return cs.folder(), cs.Slug, nil
	}
	return "", "", errors.New("there is no folder called " + strconv.Quote(folder) + " on this computer")
}

// keepWords: the choices a full sync can be asked to make, as the person reads them.
var keepWords = map[string]string{"this": "this computer's", "hub": "the hub's", "newer": "the newer"}

// KeepWords: how a keep choice reads, for the command line.
func KeepWords(keep string) string { return keepWords[keep] }

// syncable: the circle has everything a sync needs and nothing that forbids one.
func (cs CircleState) syncable() bool {
	return !cs.Removed && !cs.NeedsKey && cs.KeySealed != "" && !cs.AsidePending
}

// readmit brings back a circle that had been removed here. Its old directory stopped being its own when it was
// removed, so it is placed again like any arrival.
func (cs *CircleState) readmit() {
	cs.Removed, cs.Folder = false, ""
}

// Dir is where this circle's files are on this computer.
func (cs CircleState) Dir() string { return filepath.Join(SyncRoot(), cs.folder()) }

func (cs CircleState) folder() string {
	if cs.Folder != "" {
		return cs.Folder
	}
	return safeName(cs.DisplayName)
}

// placeFolders gives every live circle that has no directory recorded here one that no other live circle on this
// computer uses: its name, or the first free of "<name> 2", "<name> 3" and so on (docs/adr/0021). Two circles in one
// directory would each sync it with their own bucket and carry one folder's files to the other folder's members.
// Circles already placed keep their directory, and circles that were already on this computer are placed before the
// ones in arriving, so an arriving folder never takes a directory from one that was here. A circle whose directory
// changed resyncs, because bisync keys its listings by path. It returns, in config order, the slugs of the circles
// whose directory is new to them: every arrival, and a folder already here that had to move because an earlier one
// had its name. Those directories must be emptied before they sync (emptyNewFolders).
func (c *Config) placeFolders(arriving map[string]bool) []string {
	fresh := map[string]bool{}
	for _, pass := range []bool{false, true} {
		for i := range c.Circles {
			cs := &c.Circles[i]
			// a circle with no name yet (an install, before the hub's bundle arrives) is placed once it has one
			if cs.Removed || cs.Folder != "" || cs.DisplayName == "" || arriving[cs.Slug] != pass {
				continue
			}
			before := cs.folder()
			cs.Folder = c.freeFolder(i, safeName(cs.DisplayName))
			if cs.Folder != before {
				cs.Resync = true
				fresh[cs.Slug] = true
			}
		}
	}
	var out []string
	for _, cs := range c.Circles {
		if !cs.Removed && (arriving[cs.Slug] || fresh[cs.Slug]) {
			out = append(out, cs.Slug)
		}
	}
	return out
}

// freeFolder returns want, or the first of "<want> 2", "<want> 3"... that no live circle other than circle i has. A
// name that would be the place set-aside files go (setAsideRoot) gets "Folder " in front: a folder of that name
// would sync whatever is set aside into it.
func (c *Config) freeFolder(i int, want string) string {
	if len(want) >= len(setAsideName) && strings.EqualFold(want[:len(setAsideName)], setAsideName) {
		want = "Folder " + want
	}
	taken := func(name string) bool {
		for j, o := range c.Circles {
			// both names are in composed form (model.FolderName), so case is the one difference left to ignore
			if j != i && !o.Removed && o.Folder != "" && strings.EqualFold(o.Folder, name) {
				return true
			}
		}
		return false
	}
	name := want
	for n := 2; taken(name); n++ {
		name = want + " " + strconv.Itoa(n)
	}
	return name
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

// safeName is the directory a folder's name becomes under the sync root (docs/adr/0020).
func safeName(s string) string {
	if name := model.FolderName(s); name != "" {
		return name
	}
	return "Shared"
}
