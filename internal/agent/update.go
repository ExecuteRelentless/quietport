package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/model"
)

// ReleasePubKey is the ed25519 key every payload must be signed with (NFR-40). Set at build time.
var ReleasePubKey = ""

type updateState struct {
	Version  string    `json:"version"`
	Attempts int       `json:"attempts"`
	At       time.Time `json:"at"`
	Previous string    `json:"previous"`
	// Failed: why the update to Version could not be installed. Nothing was swapped, so there is no boot to check.
	Failed string `json:"failed,omitempty"`
}

// updateDue: whether to fetch version now. An update installed or attempted within the last hour is not fetched
// again, so a device whose swap fails does not download the bundle at every heartbeat (2026-09-14).
func updateDue(s updateState, ok bool, version string, now time.Time) bool {
	return !(ok && s.Version == version && now.Sub(s.At) < time.Hour)
}

func readUpdateState() (updateState, bool) {
	var s updateState
	b, err := os.ReadFile(UpdateStatePath())
	if err != nil {
		return s, false
	}
	return s, json.Unmarshal(b, &s) == nil
}

func writeUpdateState(s *updateState) {
	if s == nil {
		_ = os.Remove(UpdateStatePath())
		return
	}
	b, _ := json.Marshal(s)
	_ = os.WriteFile(UpdateStatePath(), b, 0o600)
}

// checkUpdateBoot runs first thing at start: if a just-applied update keeps failing to boot, roll back (NFR-41).
func (a *Agent) checkUpdateBoot() error {
	s, ok := readUpdateState()
	if !ok || s.Failed != "" {
		return nil
	}
	if s.Version == Version {
		if s.Attempts >= 2 {
			a.logf("update %s failed to start %d times, rolling back to %s", s.Version, s.Attempts, s.Previous)
			return a.rollback(s)
		}
		s.Attempts++
		writeUpdateState(&s)
		// cleared after the first successful heartbeat
		go func() {
			time.Sleep(3 * time.Minute)
			if st := LoadState(); st.LastHeartbeatOK && st.LastHeartbeat.After(time.Now().Add(-4*time.Minute)) {
				writeUpdateState(nil)
				for _, n := range bundleFiles() {
					_ = os.Remove(filepath.Join(AppDir(), n+".prev"))
				}
				a.logf("update %s confirmed", Version)
			}
		}()
	}
	return nil
}

func bundleFiles() []string {
	if runtime.GOOS == "windows" {
		return []string{"qpsync-agent.exe", "rclone.exe", daemonName("windows"), "tailscale.exe"}
	}
	return []string{"qpsync-agent", "rclone", daemonName(runtime.GOOS), "tailscale"}
}

func (a *Agent) rollback(s updateState) error {
	for _, n := range bundleFiles() {
		cur, prev := filepath.Join(AppDir(), n), filepath.Join(AppDir(), n+".prev")
		if _, err := os.Stat(prev); err != nil {
			continue
		}
		_ = os.Rename(cur, cur+".failed")
		_ = os.Rename(prev, cur)
	}
	writeUpdateState(nil)
	return a.reexec()
}

// applyUpdate downloads, verifies and installs a signed bundle, then restarts (NFR-40).
func (a *Agent) applyUpdate(ctx context.Context, u model.UpdateInfo) {
	if ReleasePubKey == "" {
		a.logf("update available (%s) but this build has no release key; skipping", u.Version)
		return
	}
	if u.Version == Version || !semverNewer(u.Version, Version) {
		return // never fetch what is already running
	}
	if s, ok := readUpdateState(); !updateDue(s, ok, u.Version, time.Now()) {
		return
	}
	part := filepath.Join(AppDir(), "update-"+u.Version+".part")
	a.logf("downloading update %s", u.Version)
	body, hdr, err := a.hub.Download(ctx, u.URL, part)
	if err != nil {
		a.logf("update download: %v", err)
		return
	}
	sum := cryptobox.SHA256Hex(body)
	if sum != u.SHA256 || (hdr.Get("X-Quietport-SHA256") != "" && hdr.Get("X-Quietport-SHA256") != sum) {
		a.logf("update rejected: checksum mismatch")
		_ = os.Remove(part)
		return
	}
	_ = os.Remove(part)
	if !cryptobox.Verify(ReleasePubKey, []byte(sum), u.Sig) {
		a.logf("update rejected: bad signature")
		return
	}
	stage := filepath.Join(AppDir(), "update-stage")
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return
	}
	if err := extract(body, stage); err != nil {
		a.logf("update extract: %v", err)
		return
	}
	a.syncMu.Lock() // no rclone mid-swap
	defer a.syncMu.Unlock()
	if err := swapBundle(AppDir(), stage, bundleFiles(), osSwapFS); err != nil {
		a.logf("%v; nothing was replaced, and %s is not fetched again for an hour", err, u.Version)
		writeUpdateState(&updateState{Version: u.Version, At: time.Now(), Previous: Version, Failed: err.Error()})
		_ = os.RemoveAll(stage)
		a.stMu.Lock()
		a.addCondition("update_failed:" + u.Version)
		a.stMu.Unlock()
		return
	}
	for _, extra := range []string{"qp", "qp.cmd", "qp-sidebar"} {
		if src := filepath.Join(stage, extra); fileExists(src) {
			_ = os.Rename(src, filepath.Join(AppDir(), extra))
		}
	}
	_ = os.RemoveAll(stage)
	writeUpdateState(&updateState{Version: u.Version, At: time.Now(), Previous: Version})
	a.logf("update %s installed, restarting", u.Version)
	_ = a.reexec()
}

// swapFS: the two file operations a swap makes, so a test can make one of them fail the way Windows does.
type swapFS struct {
	remove func(string) error
	rename func(from, to string) error
}

var osSwapFS = swapFS{remove: os.Remove, rename: os.Rename}

// swapBundle replaces the bundle's programs in appDir with those in stage, all of them or none (2026-09-14). Each
// program still in place becomes "<name>.prev" first, for checkUpdateBoot to roll back to. Windows will not delete
// the file of a running program but will rename it, and a daemon an earlier update left behind runs from its
// ".prev": such a backup is moved aside to "<name>.prev-<n>" instead of deleted, and removeAsideBackups clears those
// once nothing runs from them. If any step fails, every program already replaced is put back.
func swapBundle(appDir, stage string, files []string, fs swapFS) error {
	type replaced struct {
		cur    string
		hadCur bool
	}
	var done []replaced
	undo := func() {
		for i := len(done) - 1; i >= 0; i-- {
			_ = fs.remove(done[i].cur)
			if done[i].hadCur {
				_ = fs.rename(done[i].cur+".prev", done[i].cur)
			}
		}
	}
	for _, n := range files {
		src := filepath.Join(stage, n)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		cur, prev := filepath.Join(appDir, n), filepath.Join(appDir, n+".prev")
		if _, err := os.Lstat(prev); err == nil {
			if err := fs.remove(prev); err != nil {
				if err := fs.rename(prev, prev+"-"+strconv.FormatInt(time.Now().UnixNano(), 10)); err != nil {
					undo()
					return fmt.Errorf("update swap %s: the previous backup is in the way: %w", n, err)
				}
			}
		}
		_, statErr := os.Lstat(cur)
		hadCur := statErr == nil
		if hadCur {
			if err := fs.rename(cur, prev); err != nil {
				undo()
				return fmt.Errorf("update swap %s: %w", n, err)
			}
		}
		if err := fs.rename(src, cur); err != nil {
			if hadCur {
				_ = fs.rename(prev, cur)
			}
			undo()
			return fmt.Errorf("update place %s: %w", n, err)
		}
		_ = os.Chmod(cur, 0o755)
		done = append(done, replaced{cur, hadCur})
	}
	return nil
}

// removeAsideBackups deletes the backups a swap moved aside, where nothing runs from them any more.
func removeAsideBackups(appDir string) {
	aside, _ := filepath.Glob(filepath.Join(appDir, "*.prev-*"))
	for _, p := range aside {
		_ = os.Remove(p)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// reexec hands over to the (new) binary. launchd restarts us on macOS; on Windows we start the new process ourselves.
func (a *Agent) reexec() error {
	if runtime.GOOS == "windows" {
		cmd := command(AgentBin(), "run")
		if err := cmd.Start(); err != nil {
			return err
		}
	}
	os.Exit(0)
	return nil
}

func extract(b []byte, dir string) error {
	if runtime.GOOS == "windows" {
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return err
		}
		for _, f := range zr.File {
			name := filepath.Base(f.Name)
			if f.FileInfo().IsDir() || strings.HasPrefix(name, ".") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			out, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				rc.Close()
				return err
			}
			_, err = io.Copy(out, rc)
			out.Close()
			rc.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(h.Name)
		out, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		out.Close()
		if err != nil {
			return err
		}
	}
}

var _ = syscall.Getpid

// semverNewer: a > b on the first 3 numeric parts; anything beats a "dev" build.
func semverNewer(a, b string) bool {
	parts := func(s string) [3]int {
		var p [3]int
		fmt.Sscanf(strings.TrimPrefix(s, "v"), "%d.%d.%d", &p[0], &p[1], &p[2])
		return p
	}
	pa, pb := parts(a), parts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}
