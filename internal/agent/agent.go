package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
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
// workFromAppDir makes the app folder this process's working directory, creating it if it is not there. Task
// Scheduler starts the agent in C:\Windows\System32 and launchd in /, and from there a bare name in an rclone
// argument resolves inside that folder, which is how a member's folder came to be synced with System32
// (docs/adr/0013).
func workFromAppDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chdir(dir)
}

func Run(ctx context.Context) error {
	if err := workFromAppDir(AppDir()); err != nil {
		return err
	}
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
	if moved, err := migrateDaemon(AppDir(), runtime.GOOS); err != nil {
		a.logf("renaming the daemon from the previous version: %v", err)
	} else if moved {
		a.logf("renamed the daemon from the previous version to %s", daemonName(runtime.GOOS))
	}
	if n := stopLeftoverDaemons(AppDir()); n > 0 {
		a.logf("stopped %d mesh daemon(s) left running by the previous version", n)
	}
	if refreshed, err := refreshStartup(); err != nil {
		a.logf("bringing the startup entry up to date: %v", err)
	} else if refreshed {
		a.logf("startup entry brought up to date: the agent now starts again by itself if it stops")
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

	// the one member-facing control: the "Share a folder" shortcut and its loopback page
	if cfg.UIToken == "" || cfg.UIPort == 0 {
		_ = store.Update(func(c *Config) {
			if c.UIToken == "" {
				c.UIToken = cryptobox.NewToken()
			}
			if c.UIPort == 0 {
				c.UIPort = FreePort()
			}
		})
		cfg = store.Config()
	}
	if port := a.serveLocalUI(ctx, cfg.UIPort, cfg.UIToken); port != 0 {
		if port != cfg.UIPort {
			_ = store.Update(func(c *Config) { c.UIPort = port })
		}
		writeShareLink(port, cfg.UIToken)
	}

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
	watch, stop := watchRoots(a.store.Config().Circles)
	for _, dir := range stop {
		a.watcher.RemoveRoot(dir)
	}
	for dir, slug := range watch {
		_ = os.MkdirAll(dir, 0o755)
		a.watcher.AddRoot(slug, dir)
	}
}

// watchRoots: the directory of every live folder here, and the directories of removed folders that no live folder
// has taken since, whose watches stop (docs/adr/0021).
func watchRoots(circles []CircleState) (watch map[string]string, stop []string) {
	watch = map[string]string{}
	for _, c := range circles {
		if !c.Removed {
			watch[c.Dir()] = c.Slug
		}
	}
	for _, c := range circles {
		if _, live := watch[c.Dir()]; c.Removed && !live && !slices.Contains(stop, c.Dir()) {
			stop = append(stop, c.Dir())
		}
	}
	return watch, stop
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
		if c.AsidePending && !c.Removed {
			// its directory could not be emptied when it became new: try again before anything syncs it
			_ = a.store.Update(func(cf *Config) { a.emptyNewFolders(cf, []string{c.Slug}) })
			for _, cs := range a.store.Config().Circles {
				if cs.Slug == c.Slug {
					c.AsidePending = cs.AsidePending
				}
			}
		}
		if !c.syncable() {
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
	if mode != "pull" {
		ensureMarker(c.Dir())
	}
	run := func(resync string) Result { return a.rc.Sync(ctx, c, key, ak, sk, cfg.S3Endpoint, mode, resync) }
	var res Result
	if mode == "bisync" {
		var healed bool
		if res, healed = bisyncHealingTheMarker(run, c.Resync, bisyncWorkDir(c)); healed {
			a.logf("%s: the folder's marker had changed on the hub and the last sync knew of nothing else, so this cycle took a full sync keeping the hub's copy", c.Slug)
		}
	} else {
		res = run(noResync)
	}
	// rclone 1.75 aborts a bisync whose prior listing has no files ("empty prior Path1 listing"), which is exactly the
	// state of a folder nobody has put anything in yet. Nothing is corrupt: run the next cycle as a full sync (copies
	// both ways, deletes nothing), and do not count it as a failure.
	emptyListing := !res.OK && mode == "bisync" && strings.Contains(res.Output, "empty prior Path")
	if emptyListing {
		if !c.Resync {
			a.logf("%s: folder was empty at the last sync, next cycle is a full sync", c.Slug)
			a.setResync(c.Slug, true)
		}
		return
	}
	a.stMu.Lock()
	h := a.st.Circles[c.Slug]
	h.Generation = c.Generation
	if res.OK {
		h.LastSync = time.Now()
		h.LastError = ""
		h.Failures = 0
		a.st.LastSyncOK = time.Now()
		if c.Resync {
			a.setResync(c.Slug, false)
		}
	} else {
		a.st.ErrorCount++
		h.Failures++
		h.LastError = truncate(lastLine(res.Output), 200)
		a.logf("%s: sync failed (%s, %v): %s", c.Slug, mode, res.Err, truncate(ScrubPaths(tailLines(res.Output, 12)), 1500))
		if res.Quota {
			a.addCondition("quota_exceeded:" + c.Slug)
		}
		if res.PathTooLong {
			a.addCondition("path_too_long:" + c.Slug)
		}
		// a bisync that keeps aborting (new path, moved folder, lost listings) is not going to heal on its own
		if mode == "bisync" && !c.Resync && h.Failures >= 3 && strings.Contains(strings.ToLower(res.Output), "bisync aborted") {
			res.NeedsResync = true
		}
		if res.NeedsResync && mode == "bisync" && !c.Resync {
			a.logf("%s: bisync state unusable, scheduling a full resync", c.Slug) // §9 corrupt bisync state
			a.addCondition("corrupt_state:" + c.Slug)
			a.setResync(c.Slug, true)
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
		Notify("Quietport", "Your shared folders have not been able to update for more than a week. Check your internet connection, or ask the person who shared the folder with you."+".")
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
		// a config written before 0.1.26 records no directories: the folders already here take theirs first
		c.placeFolders(nil)
		arriving := map[string]bool{}
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
				arriving[cc.Slug] = true
				a.logf("circle added: %s", cc.Slug)
			} else {
				cs := &c.Circles[idx]
				if cs.Removed {
					cs.readmit()
					cs.Resync = true
					changed = true
					arriving[cc.Slug] = true
				}
				gen := cs.Generation
				if cs.Folder != "" && cs.DisplayName != "" && cs.DisplayName != cc.DisplayName {
					a.renameFolder(c, idx, cc.DisplayName)
					changed = true
				}
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
		// under the store's lock, so no sync can read a folder before its new directory is emptied
		a.emptyNewFolders(c, c.placeFolders(arriving))
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

// emptyNewFolders empties the directories new to the given circles, and logs what it moved and what it could not.
func (a *Agent) emptyNewFolders(c *Config, slugs []string) {
	if len(slugs) == 0 {
		return
	}
	today := time.Now().Format("2006-01-02")
	n, err := emptyNewFolders(c, slugs, today)
	if err != nil {
		a.logf("could not set aside everything already in a new folder, which waits until it can: %v", err)
	}
	if n > 0 {
		a.logf("moved %d entries already in new folders into %s before their first sync", n, setAsideRoot(today))
	}
}

// renameFolder follows a circle renamed on the hub: its directory takes the new name when that name is free on this
// computer, so nothing is downloaded twice. When another live folder has that name, it takes the next free one; when
// a directory of that name is already on disk it stays where it is, because moving into it would mix this folder's
// files with whatever that directory holds (docs/adr/0021).
func (a *Agent) renameFolder(c *Config, i int, displayName string) {
	cs := &c.Circles[i]
	from, to := cs.Folder, c.freeFolder(i, safeName(displayName))
	if to == from {
		return
	}
	oldDir, newDir := filepath.Join(SyncRoot(), from), filepath.Join(SyncRoot(), to)
	if _, err := os.Stat(newDir); err == nil {
		a.logf("%s: renamed to %q; its folder stays %q because %q is already there", cs.Slug, displayName, from, to)
		return
	}
	if a.watcher != nil {
		a.watcher.RemoveRoot(oldDir)
	}
	if _, err := os.Stat(oldDir); err == nil {
		if err := os.Rename(oldDir, newDir); err != nil {
			a.logf("%s: could not move folder %q to %q: %v", cs.Slug, from, to, err)
			return
		}
	}
	cs.Folder = to
	cs.Resync = true // bisync listings are keyed by path
	a.logf("%s: folder renamed to %q", cs.Slug, to)
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
		Notify("Quietport", "A shared folder's key was changed and this computer needs a new invitation. Ask the folder's owner for a new link."+".")
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

// setResync records whether the circle's next cycle is a full sync.
func (a *Agent) setResync(slug string, on bool) {
	_ = a.store.Update(func(cf *Config) {
		for i := range cf.Circles {
			if cf.Circles[i].Slug == slug {
				cf.Circles[i].Resync = on
			}
		}
	})
}

// markerTime is the modification time of every new marker on every device (docs/adr/0022). A marker stamped with the
// moment each device wrote it differs between devices, and in a folder holding nothing else the second member's
// first full sync replaced it on the hub, after which rclone refused every sync on the first member's device because
// all of that folder's files had changed.
var markerTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

const markerText = "Quietport keeps this file here so the folder stays in sync even while it is empty.\n"

// ensureMarker keeps the marker in a folder. A new marker is the same file on every device, time included; a marker
// already there keeps its content and time, because it is the copy the hub has and a change to it is a change bisync
// counts. It is hidden again on Windows, where one that arrived in a sync was written as an ordinary file.
func ensureMarker(dir string) {
	p := filepath.Join(dir, MarkerFile)
	if _, err := os.Stat(p); err == nil {
		hideFile(p)
		return
	}
	if _, err := os.Stat(dir); err != nil {
		return // folder not there yet (or gone); nothing to mark
	}
	if err := os.WriteFile(p, []byte(markerText), 0o644); err == nil {
		_ = os.Chtimes(p, markerTime, markerTime)
		hideFile(p)
	}
}

// bisyncHealingTheMarker runs one bisync cycle through run, as a full sync keeping this device's copy when resync is
// set. When rclone refuses the cycle over the marker alone it runs a full sync keeping the hub's copy at once, and
// that result is the cycle's (docs/adr/0022). A device therefore never pushes its own marker at the others: every
// 0.1.26 device takes the hub's.
func bisyncHealingTheMarker(run func(resync string) Result, resync bool, work string) (res Result, healed bool) {
	if resync {
		return run(resyncThisDevice), false
	}
	if res = run(noResync); res.OK || !refusalOverTheMarkerAlone(res.Output, work) {
		return res, false
	}
	return run(resyncKeepHub), true
}

// refusalOverTheMarkerAlone: rclone refused a bisync because every file changed, and every listing it kept from its
// last good run knew of nothing but the marker (docs/adr/0022). The refusal can then only be about the marker, which
// a full sync elsewhere replaced on the hub, and a full sync has nothing it could delete or bring back: it sends up
// what the member added since and brings down what others added.
func refusalOverTheMarkerAlone(output, work string) bool {
	return strings.Contains(output, "all files were changed") && listingsKnowOnlyTheMarker(work)
}

// listingsKnowOnlyTheMarker: bisync's listings in work exist and name no file but the marker. A listing that cannot
// be read or parsed counts as knowing more.
func listingsKnowOnlyTheMarker(work string) bool {
	var lists []string
	for _, side := range []string{"*.path1.lst", "*.path2.lst"} {
		m, _ := filepath.Glob(filepath.Join(work, side))
		lists = append(lists, m...)
	}
	if len(lists) == 0 {
		return false
	}
	for _, l := range lists {
		b, err := os.ReadFile(l)
		if err != nil {
			return false
		}
		for _, line := range strings.Split(string(b), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			i := strings.IndexByte(line, '"')
			if i < 0 {
				return false
			}
			if name, err := strconv.Unquote(line[i:]); err != nil || name != MarkerFile {
				return false
			}
		}
	}
	return true
}
