package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
	if !ok {
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
	if s, ok := readUpdateState(); ok && s.Version == u.Version && time.Since(s.At) < time.Hour {
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
	for _, n := range bundleFiles() {
		src := filepath.Join(stage, n)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		cur := filepath.Join(AppDir(), n)
		_ = os.Remove(cur + ".prev")
		if err := os.Rename(cur, cur+".prev"); err != nil && !errors.Is(err, os.ErrNotExist) {
			a.logf("update swap %s: %v", n, err)
			return
		}
		if err := os.Rename(src, cur); err != nil {
			_ = os.Rename(cur+".prev", cur)
			a.logf("update place %s: %v", n, err)
			return
		}
		_ = os.Chmod(cur, 0o755)
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
