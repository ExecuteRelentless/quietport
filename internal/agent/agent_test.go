package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSemverNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.14", "0.1.13", true},
		{"0.1.13", "0.1.14", false},
		{"0.1.13", "0.1.13", false},
		{"0.2.0", "0.1.99", true},
		{"1.0.0", "0.9.9", true},
		{"v0.1.14", "0.1.13", true},
		{"0.1.1", "dev", true}, // a dev build takes any release
	}
	for _, c := range cases {
		if got := semverNewer(c.a, c.b); got != c.want {
			t.Errorf("semverNewer(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

// The marker keeps bisync away from its empty-listing abort (docs/adr/0004): it is created in an existing folder,
// left alone when present, and never created for a folder that does not exist (that would resurrect a removed one).
func TestEnsureMarker(t *testing.T) {
	dir := t.TempDir()
	ensureMarker(dir)
	p := filepath.Join(dir, MarkerFile)
	st, err := os.Stat(p)
	if err != nil || st.Size() == 0 {
		t.Fatalf("marker not created: %v", err)
	}
	_ = os.WriteFile(p, []byte("kept"), 0o644)
	ensureMarker(dir)
	b, _ := os.ReadFile(p)
	if string(b) != "kept" {
		t.Fatal("existing marker was overwritten")
	}
	gone := filepath.Join(dir, "removed-folder")
	ensureMarker(gone)
	if _, err := os.Stat(gone); err == nil {
		t.Fatal("marker created a folder that did not exist")
	}
}

// The default excludes drop ".qp-*"; the marker must not fall under any of them or it never reaches the hub.
func TestMarkerIsNotExcluded(t *testing.T) {
	for _, pat := range defaultExcludes {
		if ok, _ := filepath.Match(pat, MarkerFile); ok {
			t.Fatalf("exclude %q matches the marker %q", pat, MarkerFile)
		}
	}
}

// Install must wait for tailscaled to answer on its socket rather than sleep a fixed 1.5 s (docs/adr/0010): on
// Windows the daemon's pipe appears some time after launch, and a daemon that dies must be reported as such.
func TestWaitForDaemon(t *testing.T) {
	calls := 0
	probe := func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("not yet")
		}
		return nil
	}
	if err := waitForDaemon(context.Background(), probe, 2*time.Second, 5*time.Millisecond); err != nil {
		t.Fatalf("daemon that answers on the third poll: %v", err)
	}
	if calls != 3 {
		t.Fatalf("polled %d times, want 3", calls)
	}
	never := func(context.Context) error { return errors.New("down") }
	start := time.Now()
	err := waitForDaemon(context.Background(), never, 40*time.Millisecond, 5*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "down") {
		t.Fatalf("daemon that never answers: err=%v, want the last probe error", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("waited %s past a 40 ms deadline", time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForDaemon(ctx, never, time.Hour, 5*time.Millisecond); err == nil {
		t.Fatal("cancelled context: want an error, got nil")
	}
}

// On Windows tailscaled drops its profile and goes idle whenever no client is connected to its pipe, unless it runs
// in Unattended Mode (docs/adr/0011). The agent only connects for a moment per status call, so without the flag the
// SOCKS proxy is dead between calls and nothing logs back in after a restart. Other platforms have no such mode.
func TestUpArgsUnattendedOnWindows(t *testing.T) {
	has := func(args []string, flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	if args := upArgs("windows", "https://hub.example", "key", "pc"); !has(args, "--unattended") {
		t.Fatalf("windows: %q lacks --unattended", args)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if args := upArgs(goos, "https://hub.example", "key", "mac"); has(args, "--unattended") {
			t.Fatalf("%s: %q has --unattended, a Windows-only flag", goos, args)
		}
	}
}

// A failed or earlier install leaves tailscaled's state behind. Reusing it made the next install register under the
// old node key, which the hub already held for the first attempt, so the hub dropped the new node's traffic and the
// install failed at enrolment (docs/adr/0012). Every install must start from an empty state directory.
func TestResetMeshState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ts")
	_ = os.MkdirAll(filepath.Join(dir, "profile-a3ed"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "tailscaled.state"), []byte(`{"_machinekey":"old"}`), 0o600)
	if err := resetMeshState(dir); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("state directory survived the reset (stat err=%v)", err)
	}
	if err := resetMeshState(filepath.Join(t.TempDir(), "never-created")); err != nil {
		t.Fatalf("missing directory: %v", err)
	}
}

// On Windows rclone reads a one-letter name before a colon as a drive letter, so the crypt remote "C:" was the C
// drive: the agent's current directory there, which Task Scheduler sets to C:\Windows\System32. A member's folder was
// bisynced with System32 instead of the hub (docs/adr/0013). Elsewhere the name stays "C": bisync keys its listings
// by it, and renaming it would force every running Mac into a resync.
func TestCryptRemoteIsNotADriveLetter(t *testing.T) {
	if n := cryptName("windows"); len(n) < 2 {
		t.Fatalf("windows: crypt remote %q is a drive letter to rclone", n)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if n := cryptName(goos); n != "C" {
			t.Fatalf("%s: crypt remote renamed to %q; existing bisync listings are keyed by \"C\"", goos, n)
		}
	}
}

// tailscaled resolves the control server with Go's own DNS client on Windows, and Go adds an EDNS0 record to every
// query. Some DNS forwarders mangle the reply to such a query (VMware Fusion's NAT DNS on macOS returns a malformed
// packet), so the lookup failed, `tailscale up` hung until the agent's timeout and the install failed, while Windows'
// own resolver, which the installer uses, worked (docs/adr/0014). On Windows tailscaled runs with GODEBUG=netedns0=0.
func TestTailscaledEnvDisablesEDNSOnWindows(t *testing.T) {
	get := func(env []string) string {
		for _, e := range env {
			if strings.HasPrefix(e, "GODEBUG=") {
				return strings.TrimPrefix(e, "GODEBUG=")
			}
		}
		return ""
	}
	if g := get(tsEnv("windows", []string{"PATH=x"})); g != "netedns0=0" {
		t.Fatalf("windows: GODEBUG=%q, want netedns0=0", g)
	}
	if g := get(tsEnv("windows", []string{"GODEBUG=http2client=0", "PATH=x"})); g != "http2client=0,netedns0=0" {
		t.Fatalf("windows with an existing GODEBUG: %q, want it kept and netedns0=0 added", g)
	}
	if g := get(tsEnv("darwin", []string{"PATH=x"})); g != "" {
		t.Fatalf("darwin: GODEBUG=%q, want none (tailscaled uses the system resolver there)", g)
	}
}
