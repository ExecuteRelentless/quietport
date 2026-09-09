package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/model"
)

var Version = "dev"

// Agent is the long-running client process (`qpsync-agent run`).
type Agent struct {
	store   *Store
	ts      *TS
	rc      *Rclone
	hub     *HubClient
	st      State
	stMu    sync.Mutex
	logger  *log.Logger
	watcher *Watcher
	syncNow chan string
	syncMu  sync.Mutex
	hubIP   string
}

func newLogger() *log.Logger {
	_ = os.MkdirAll(LogDir(), 0o700)
	lj := &lumberjack.Logger{Filename: filepath.Join(LogDir(), "agent.log"), MaxSize: 10, MaxBackups: 4, MaxAge: 90, Compress: false} // ~50 MB cap (FR-56)
	return log.New(lj, "", log.LstdFlags)
}

func (a *Agent) logf(format string, args ...any) { a.logger.Printf(format, args...) }

// Run is the main loop: tailscaled supervision, sync scheduler, heartbeat, versions pruning, disk watch.
func Run(ctx context.Context) error {
	store, err := OpenStore()
	if err != nil {
		return err
	}
	cfg := store.Config()
	if cfg.DeviceID == 0 || cfg.SocksPort == 0 {
		return fmt.Errorf("not installed")
	}
	a := &Agent{store: store, ts: NewTS(cfg.SocksPort), logger: newLogger(), syncNow: make(chan string, 16), st: LoadState()}
	a.rc = &Rclone{bin: RcloneBin(), proxy: a.ts.ProxyURL(), logf: a.logf}
	a.st.StartedAt = time.Now()
	a.st.Conditions = nil
	a.saveState()
	a.logf("agent %s starting (%s/%s)", Version, runtime.GOOS, runtime.GOARCH)
	a.hubIP = hostOf(cfg.HubAPI)

	if err := a.checkUpdateBoot(); err != nil {
		a.logf("update boot check: %v", err)
	}

	go a.ts.Run(ctx, a.logf)
	if st, err := a.ts.WaitRunning(ctx, 90*time.Second); err != nil {
		a.logf("mesh: %v (state %s); will keep trying", err, st.BackendState)
		if st.BackendState == "NeedsLogin" {
			a.tryRelogin(ctx)
		}
	}

	hub, err := NewHubClient(cfg.HubAPI, store.DeviceToken(), a.ts.ProxyURL())
	if err != nil {
		return err
	}
	a.hub = hub

	a.watcher, err = NewWatcher(5 * time.Second)
	if err != nil {
		a.logf("watcher: %v", err)
	}
	a.refreshWatches()

	// first heartbeat immediately so config/grants are fresh, then every 5 minutes (FR-55/63)
	if !a.heartbeat(ctx) {
		go func() { // the mesh path can take a few seconds after Running before the hub is reachable
			for _, d := range []time.Duration{15 * time.Second, 30 * time.Second, 60 * time.Second} {
				time.Sleep(d)
				if a.heartbeat(ctx) {
					return
				}
			}
		}()
	}
	go a.heartbeatLoop(ctx)
	go a.pruneLoop(ctx)
	a.syncLoop(ctx)
	return nil
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	if i := strings.Index(u, ":"); i > 0 {
		return u[:i]
	}
	return u
}

func (a *Agent) saveState() {
	a.stMu.Lock()
	SaveState(a.st)
	a.stMu.Unlock()
}

func (a *Agent) refreshWatches() {
	if a.watcher == nil {
		return
	}
	for _, c := range a.store.Config().Circles {
		dir := CircleDir(c.DisplayName)
		if c.Removed {
			a.watcher.RemoveRoot(dir)
			continue
		}
		_ = os.MkdirAll(dir, 0o755)
		a.watcher.AddRoot(c.Slug, dir)
	}
}

// --- sync scheduling (FR-30/31/32) ---

func (a *Agent) syncLoop(ctx context.Context) {
	interval := func() time.Duration {
		if n := a.store.Config().SyncInterval; n > 0 {
			return time.Duration(n) * time.Second
		}
		return time.Duration(model.DefaultSyncSeconds) * time.Second
	}
	timer := time.NewTimer(5 * time.Second)
	var trig <-chan string
	if a.watcher != nil {
		trig = a.watcher.Trigger()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			a.syncAll(ctx, "")
			timer.Reset(interval())
		case slug := <-trig:
			a.syncAll(ctx, slug)
		case slug := <-a.syncNow:
			a.syncAll(ctx, slug)
		}
	}
}

// syncAll runs circles one at a time (FR-32). only="" means every circle.
func (a *Agent) syncAll(ctx context.Context, only string) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	cfg := a.store.Config()
	if a.pausedForDisk() {
		return
	}
	circles := append([]CircleState(nil), cfg.Circles...)
	sort.Slice(circles, func(i, j int) bool { return circles[i].Slug < circles[j].Slug })
	for _, c := range circles {
		if only != "" && c.Slug != only {
			continue
		}
		if c.Removed || c.NeedsKey || c.KeySealed == "" {
			continue
		}
		a.syncCircle(ctx, c)
		if ctx.Err() != nil {
			return
		}
	}
}

func (a *Agent) effectiveMode(c CircleState) string {
	switch {
	case c.Role == "readonly":
		return "pull"
	case c.SyncMode == model.ModeSendOnly:
		return "push"
	case c.SyncMode == model.ModeReceiveOnly:
		return "pull"
	}
	return "bisync"
}

func (a *Agent) syncCircle(ctx context.Context, c CircleState) {
	key, err := a.store.CircleKey(c)
	if err != nil {
		a.logf("%s: key unavailable: %v", c.Slug, err)
		return
	}
	ak, sk := a.store.CircleS3(c)
	cfg := a.store.Config()
	mode := a.effectiveMode(c)
	res := a.rc.Sync(ctx, c, key, ak, sk, cfg.S3Endpoint, mode, c.Resync)
	a.stMu.Lock()
	h := a.st.Circles[c.Slug]
	h.Generation = c.Generation
	if res.OK {
		h.LastSync = time.Now()
		h.LastError = ""
		a.st.LastSyncOK = time.Now()
		if c.Resync {
			_ = a.store.Update(func(cf *Config) {
				for i := range cf.Circles {
					if cf.Circles[i].Slug == c.Slug {
						cf.Circles[i].Resync = false
					}
				}
			})
		}
	} else {
		a.st.ErrorCount++
		h.LastError = truncate(lastLine(res.Output), 200)
		a.logf("%s: sync failed (%s, %v): %s", c.Slug, mode, res.Err, truncate(ScrubPaths(tailLines(res.Output, 12)), 1500))
		if res.Quota {
			a.addCondition("quota_exceeded:" + c.Slug)
		}
		if res.PathTooLong {
			a.addCondition("path_too_long:" + c.Slug)
		}
		if res.NeedsResync && mode == "bisync" && !c.Resync {
			a.logf("%s: bisync state unusable, scheduling a full resync", c.Slug) // §9 corrupt bisync state
			a.addCondition("corrupt_state:" + c.Slug)
			_ = a.store.Update(func(cf *Config) {
				for i := range cf.Circles {
					if cf.Circles[i].Slug == c.Slug {
						cf.Circles[i].Resync = true
					}
				}
			})
		}
	}
	a.st.Circles[c.Slug] = h
	a.stMu.Unlock()
	a.saveState()
}

func (a *Agent) addCondition(c string) {
	for _, x := range a.st.Conditions {
		if x == c {
			return
		}
	}
	a.st.Conditions = append(a.st.Conditions, c)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return ScrubPaths(lines[i])
		}
	}
	return ""
}
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// pausedForDisk: FR-57 first bullet, and §9 "local disk full".
func (a *Agent) pausedForDisk() bool {
	free := FreeDisk(SyncRoot())
	if free >= 0 && free < 1<<30 {
		a.stMu.Lock()
		a.st.Paused = "disk"
		a.addCondition("disk_low")
		notify := time.Since(a.st.Notified["disk"]) > 24*time.Hour
		if notify {
			a.st.Notified["disk"] = time.Now()
		}
		a.stMu.Unlock()
		if notify {
			Notify("Quietport", "This computer has less than 1 GB of free space, so your shared folders have paused. They will resume when space is freed.")
		}
		a.saveState()
		return true
	}
	a.stMu.Lock()
	if a.st.Paused == "disk" {
		a.st.Paused = ""
	}
	a.stMu.Unlock()
	return false
}

// --- heartbeat (FR-55) + config application (FR-63) ---

func (a *Agent) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(model.HeartbeatInterval)
	defer t.Stop()
	backoff := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !a.heartbeat(ctx) {
				// FR-24: hub unreachable -> retry with backoff, capped at 5 minutes, inside the 5-minute cadence
				if backoff == 0 {
					backoff = 30 * time.Second
				} else if backoff < 5*time.Minute {
					backoff *= 2
				}
				time.AfterFunc(backoff, func() { a.heartbeat(ctx) })
			} else {
				backoff = 0
			}
		}
	}
}

func (a *Agent) heartbeat(ctx context.Context) bool {
	_ = a.store.Config()
	tsst, _ := a.ts.Status(ctx, a.hubIP)
	conn := "down"
	if tsst.BackendState == "Running" {
		conn = "direct"
		if tsst.HubRelayed {
			conn = "relayed" // FR-23
		}
	}
	a.stMu.Lock()
	hb := model.Heartbeat{AgentVersion: Version, OS: runtime.GOOS + "/" + runtime.GOARCH, ConnectionType: conn, PerCircle: map[string]model.CircleHealth{},
		FreeDisk: FreeDisk(SyncRoot()), ErrorCount: a.st.ErrorCount, Conditions: append([]string(nil), a.st.Conditions...), ClientTime: time.Now().UTC(), PendingBytes: a.st.PendingBytes}
	for k, v := range a.st.Circles {
		hb.PerCircle[k] = v
	}
	a.st.ConnectionType = conn
	a.st.TailnetIP = tsst.TailnetIP
	a.stMu.Unlock()

	bundle, err := a.hub.Heartbeat(ctx, hb)
	a.stMu.Lock()
	a.st.LastHeartbeat = time.Now()
	a.st.LastHeartbeatOK = err == nil
	if err == nil {
		a.st.Conditions = nil
	}
	a.stMu.Unlock()
	a.saveState()
	if err != nil {
		a.logf("heartbeat: %v", err)
		if he, ok := err.(*HubError); ok && he.Code == 403 {
			a.logf("this device has been revoked; sync stopped")
			_ = a.store.Update(func(c *Config) {
				for i := range c.Circles {
					c.Circles[i].Removed = true
				}
			})
		}
		if tsst.BackendState == "NeedsLogin" {
			a.tryRelogin(ctx)
		}
		a.checkSilentFailure()
		return false
	}
	a.applyBundle(ctx, bundle)
	a.checkSilentFailure()
	if bundle.Update != nil {
		go a.applyUpdate(ctx, *bundle.Update)
	}
	return true
}

// tryRelogin: if the mesh session is gone and we still hold the pre-auth key (first boot), use it.
func (a *Agent) tryRelogin(ctx context.Context) {
	cfg := a.store.Config()
	var pak string
	_ = cfg
	if err := a.store.Open(cfg.PreAuthSealed, &pak); err == nil && pak != "" {
		if err := a.ts.Up(ctx, cfg.LoginServer, pak); err == nil {
			_ = a.store.Update(func(c *Config) { c.PreAuthSealed = "" })
			a.logf("mesh login completed")
			return
		}
	}
}

// checkSilentFailure: FR-57 third bullet.
func (a *Agent) checkSilentFailure() {
	a.stMu.Lock()
	defer a.stMu.Unlock()
	last := a.st.LastSyncOK
	if last.IsZero() {
		last = a.st.StartedAt
	}
	if time.Since(last) > 7*24*time.Hour && time.Since(a.st.Notified["stale"]) > 24*time.Hour {
		a.st.Notified["stale"] = time.Now()
		Notify("Quietport", "Your shared folders have not been able to update for more than a week. Please contact "+a.store.Config().OperatorName+".")
	}
}

func (a *Agent) applyBundle(ctx context.Context, b model.ConfigBundle) {
	keys, kerr := a.store.DeviceKeys()
	changed := false
	err := a.store.Update(func(c *Config) {
		if b.S3Endpoint != "" && c.S3Endpoint != b.S3Endpoint {
			c.S3Endpoint = b.S3Endpoint
			changed = true
		}
		if b.SyncInterval > 0 {
			c.SyncInterval = b.SyncInterval
		}
		if b.SupportContact != "" {
			c.SupportContact = b.SupportContact
		}
		seen := map[string]bool{}
		for _, cc := range b.Circles {
			seen[cc.Slug] = true
			idx := -1
			for i := range c.Circles {
				if c.Circles[i].Slug == cc.Slug {
					idx = i
				}
			}
			s3, _ := a.store.Seal([2]string{cc.S3AccessKey, cc.S3SecretKey})
			if idx < 0 {
				c.Circles = append(c.Circles, CircleState{CircleConfig: cc, S3Sealed: s3, Resync: true})
				idx = len(c.Circles) - 1
				changed = true
				a.logf("circle added: %s", cc.Slug)
			} else {
				cs := &c.Circles[idx]
				if cs.Removed {
					cs.Removed = false
					cs.Resync = true
					changed = true
				}
				gen := cs.Generation
				cs.CircleConfig = cc
				cs.S3Sealed = s3
				if cc.Generation != gen {
					cs.Resync = true // new ciphertext tree
					changed = true
				}
			}
			cs := &c.Circles[idx]
			// key for the current generation: from a sealed grant if we do not hold it yet
			if cs.KeyGen != cc.Generation || cs.KeySealed == "" {
				got := false
				for _, g := range b.Grants {
					if g.Slug == cc.Slug && g.Generation == cc.Generation && kerr == nil {
						var ck model.CircleKey
						if err := cryptobox.OpenFromDevice(keys, g.SealedBox, &ck); err == nil {
							cs.KeySealed, _ = a.store.Seal(ck)
							cs.KeyGen = cc.Generation
							cs.NeedsKey = false
							cs.Resync = true
							got = true
							changed = true
							a.logf("%s: received key for generation %d", cc.Slug, cc.Generation)
						}
					}
				}
				if !got && cs.KeyGen != cc.Generation {
					if !cs.NeedsKey {
						cs.NeedsKey = true
						changed = true
						a.notifyReprovision()
					}
				}
			}
		}
		for i := range c.Circles {
			if !seen[c.Circles[i].Slug] && !c.Circles[i].Removed {
				c.Circles[i].Removed = true
				changed = true
				a.logf("circle removed: %s (folder left in place)", c.Circles[i].Slug)
			}
		}
	})
	if err != nil {
		a.logf("apply config: %v", err)
	}
	if changed {
		a.refreshWatches()
		select {
		case a.syncNow <- "":
		default:
		}
	}
}

// notifyReprovision: FR-57 second bullet.
func (a *Agent) notifyReprovision() {
	a.stMu.Lock()
	ok := time.Since(a.st.Notified["reprovision"]) > 24*time.Hour
	if ok {
		a.st.Notified["reprovision"] = time.Now()
	}
	a.stMu.Unlock()
	if ok {
		Notify("Quietport", "A shared folder's key was changed and this computer needs a new invitation. Please contact "+a.store.Config().OperatorName+".")
	}
}

// pruneLoop: daily version pruning (FR-34) and the hourly long-path check (FR-40).
func (a *Agent) pruneLoop(ctx context.Context) {
	daily := time.NewTicker(24 * time.Hour)
	hourly := time.NewTicker(time.Hour)
	defer daily.Stop()
	defer hourly.Stop()
	time.Sleep(2 * time.Minute)
	a.prune(ctx)
	a.longPaths(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-daily.C:
			a.prune(ctx)
		case <-hourly.C:
			a.longPaths(ctx)
		}
	}
}

func (a *Agent) prune(ctx context.Context) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	cfg := a.store.Config()
	for _, c := range cfg.Circles {
		if c.Removed || c.KeySealed == "" {
			continue
		}
		key, err := a.store.CircleKey(c)
		if err != nil {
			continue
		}
		ak, sk := a.store.CircleS3(c)
		_ = a.rc.PruneVersions(ctx, c, key, ak, sk, cfg.S3Endpoint, c.VersionRetentionDays)
	}
}

func (a *Agent) longPaths(ctx context.Context) {
	cfg := a.store.Config()
	for _, c := range cfg.Circles {
		if c.Removed || c.KeySealed == "" {
			continue
		}
		key, err := a.store.CircleKey(c)
		if err != nil {
			continue
		}
		ak, sk := a.store.CircleS3(c)
		bad, err := a.rc.RemoteTooLong(ctx, c, key, ak, sk, cfg.S3Endpoint)
		if err != nil {
			continue
		}
		if len(bad) > 0 {
			a.logf("%s: %d remote paths exceed this platform's limit and are excluded", c.Slug, len(bad))
			a.stMu.Lock()
			a.addCondition(fmt.Sprintf("path_too_long:%s:%d", c.Slug, len(bad)))
			a.stMu.Unlock()
		}
		slug := c.Slug
		_ = a.store.Update(func(cf *Config) {
			for i := range cf.Circles {
				if cf.Circles[i].Slug == slug {
					if strings.Join(cf.Circles[i].Excluded, "\n") != strings.Join(bad, "\n") {
						cf.Circles[i].Excluded = bad
						cf.Circles[i].Resync = cf.Circles[i].Resync || a.effectiveMode(cf.Circles[i]) == "bisync"
					}
				}
			}
		})
	}
}
