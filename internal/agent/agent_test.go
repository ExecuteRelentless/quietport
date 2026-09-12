package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
	if args := upArgs("windows", "https://hub.example", "key", "pc"); !slices.Contains(args, "--unattended") {
		t.Fatalf("windows: %q lacks --unattended", args)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if args := upArgs(goos, "https://hub.example", "key", "mac"); slices.Contains(args, "--unattended") {
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
// drive: the agent's working directory, which Task Scheduler sets to C:\Windows\System32 (docs/adr/0013). Elsewhere
// it stays "C", the name bisync keys its listings by, and renaming it would force every running Mac into a resync.
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

// tailscaled resolves the control server with Go's own DNS client on Windows, and Go adds an EDNS0 record that some
// forwarders answer with a malformed packet (VMware Fusion's NAT DNS among them): the lookup failed, `tailscale up`
// hung and the install failed, while the installer, on Windows' own resolver, worked (docs/adr/0014).
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
	// Windows treats environment variable names case-insensitively, so a setting already there can arrive under any
	// spelling. Missing it would leave the machine's own setting in place and add a second, contradictory entry.
	env := tsEnv("windows", []string{"Godebug=http2client=0", "PATH=x"})
	n := 0
	for _, e := range env {
		if strings.HasPrefix(strings.ToUpper(e), "GODEBUG=") {
			n++
		}
	}
	if g := get(env); g != "http2client=0,netedns0=0" || n != 1 {
		t.Fatalf("windows with GODEBUG spelled differently: %q across %d entries, want http2client=0,netedns0=0 in 1", g, n)
	}
	if g := get(tsEnv("darwin", []string{"PATH=x"})); g != "" {
		t.Fatalf("darwin: GODEBUG=%q, want none (tailscaled uses the system resolver there)", g)
	}
}

// Half of docs/adr/0013: the agent must leave the folder it was started in. Task Scheduler starts it in
// C:\Windows\System32, and rclone reads a bare name as a path relative to the current directory.
func TestWorkFromAppDir(t *testing.T) {
	t.Chdir(t.TempDir()) // stands in for System32
	dir := filepath.Join(t.TempDir(), "app")
	if err := workFromAppDir(dir); err != nil {
		t.Fatalf("work from %s: %v", dir, err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := filepath.EvalSymlinks(wd)
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Fatalf("working directory is %s, want %s", got, want)
	}
}

// An install sets every folder to resync, which sends anything held locally up to the hub and on to every member.
// Content already sitting in the folder is of unknown provenance, so it moves aside first (docs/adr/0016).
func TestSetAside(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Shared")
	aside := filepath.Join(root, "Previous files", "Shared")
	if err := os.MkdirAll(filepath.Join(dir, "Notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, VersionsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, b := range map[string]string{"budget.xlsx": "numbers", filepath.Join("Notes", "a.txt"): "note", MarkerFile: "marker"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n, err := setAside(dir, aside)
	if err != nil || n != 2 {
		t.Fatalf("moved %d entries (err %v), want 2: the file and the folder", n, err)
	}
	if b, err := os.ReadFile(filepath.Join(aside, "Notes", "a.txt")); err != nil || string(b) != "note" {
		t.Fatalf("the folder did not move with its contents: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "budget.xlsx")); !os.IsNotExist(err) {
		t.Fatalf("a member file is still in the folder about to sync: %v", err)
	}
	for _, keep := range []string{MarkerFile, VersionsDir} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Fatalf("%s belongs to Quietport and must stay: %v", keep, err)
		}
	}
	second := filepath.Join(root, "Previous files 2", "Shared")
	if n, err := setAside(dir, second); n != 0 || err != nil {
		t.Fatalf("nothing of the member's left: moved %d (err %v), want 0", n, err)
	}
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatal("an empty set-aside folder was created")
	}
	if n, err := setAside(filepath.Join(root, "never-there"), aside); n != 0 || err != nil {
		t.Fatalf("folder that does not exist: moved %d (err %v), want 0", n, err)
	}
}

// Windows names the daemon after the product: the firewall prompt a member answers names the file that listens, and
// "tailscaled.exe" means nothing to them (docs/adr/0015). Updating from 0.1.20 leaves the old file behind, because
// that release's updater only swaps the names it knows, so the agent renames it on the next start.
func TestDaemonNameAndMigration(t *testing.T) {
	if n := daemonName("windows"); n == "tailscaled.exe" || !strings.HasSuffix(n, ".exe") {
		t.Fatalf("windows daemon file is %q; want a Quietport name ending in .exe", n)
	}
	if n := daemonName("darwin"); n != "tailscaled" {
		t.Fatalf("darwin daemon file renamed to %q; the prompt only exists on Windows", n)
	}
	dir := t.TempDir()
	legacy := filepath.Join(dir, "tailscaled.exe")
	if err := os.WriteFile(legacy, []byte("old daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	moved, err := migrateDaemon(dir, "windows")
	if err != nil || !moved {
		t.Fatalf("migrate: moved=%v err=%v, want it renamed", moved, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, daemonName("windows"))); err != nil || string(b) != "old daemon" {
		t.Fatalf("daemon not renamed with its contents: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("the old file is still there: %v", err)
	}
	if moved, err := migrateDaemon(dir, "windows"); moved || err != nil {
		t.Fatalf("second run: moved=%v err=%v, want a no-op", moved, err)
	}
	mac := t.TempDir()
	if err := os.WriteFile(filepath.Join(mac, "tailscaled.exe"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if moved, _ := migrateDaemon(mac, "darwin"); moved {
		t.Fatal("darwin: nothing to migrate")
	}
}

// Stopping by image name was wrong twice over: after the rename "tailscaled.exe" is no longer this product's daemon
// but may be a real Tailscale install, and "qpsync-agent.exe" matched the process doing the stopping, which killed
// an install halfway through (docs/adr/0015). What to stop is decided by path, and never includes the caller.
func TestOwnProcessesStopOnlyThisInstall(t *testing.T) {
	const app = `C:\Users\sam\AppData\Local\Quietport`
	procs := []proc{
		{PID: 11, Exe: app + `\` + daemonName("windows")},
		{PID: 12, Exe: app + `\tailscaled.exe`},                                     // a daemon left running by 0.1.20
		{PID: 13, Exe: `c:\users\sam\appdata\local\quietport\qpsync-agent.exe`},     // Windows varies the case
		{PID: 14, Exe: `C:\Program Files\Tailscale\tailscaled.exe`},                 // somebody else's Tailscale
		{PID: 15, Exe: `C:\Users\sam\AppData\Local\Quietport-old\qpsync-agent.exe`}, // a sibling folder, not ours
		{PID: 16, Exe: `C:\Users\sam\Downloads\Quietport.exe`},                      // the installer asking for the stop
		{PID: 17, Exe: app + `\qpsync-agent.exe`},                                   // the agent uninstalling itself
	}
	got := ownProcesses(procs, app, 17)
	if want := []int{11, 12, 13}; !slices.Equal(got, want) {
		t.Fatalf("stopping %v, want %v", got, want)
	}
	if len(ownProcesses(nil, app, 17)) != 0 {
		t.Fatal("nothing running: want nothing stopped")
	}
}

// A self-update hands over to the new agent but never stops the daemon the old one started, so the device would keep
// running the daemon the update just replaced, and the new agent's daemon would be starting against a socket that is
// already taken. The new agent stops the daemons of this install, its own and any left under the pre-0.1.21 name.
func TestLeftoverDaemonsAfterUpdate(t *testing.T) {
	const app = `C:\Users\sam\AppData\Local\Quietport`
	procs := []proc{
		{PID: 21, Exe: app + `\` + daemonName("windows")},
		{PID: 22, Exe: app + `\tailscaled.exe`},                     // started before the rename
		{PID: 23, Exe: app + `\qpsync-agent.exe`},                   // an agent: not this function's business
		{PID: 24, Exe: `C:\Program Files\Tailscale\tailscaled.exe`}, // somebody else's Tailscale
	}
	if got, want := leftoverDaemons(procs, app), []int{21, 22}; !slices.Equal(got, want) {
		t.Fatalf("stopping %v, want %v", got, want)
	}
}

// An agent that stops for any reason stayed stopped until the next logon: the Scheduled Task's only trigger was a
// logon trigger, and RestartOnFailure covers a run that fails, not one that was ended. That is what turned a stray
// console window into ten minutes of lost syncing on 2026-09-11. The trigger now repeats, so a stopped agent comes
// back within minutes, and IgnoreNew keeps the repetition from starting a second one.
func TestTaskRepeatsSoAStoppedAgentComesBack(t *testing.T) {
	x := taskXML(`WORKGROUP\sam`, `C:\Users\sam\AppData\Local\Quietport\qpsync-agent.exe`)
	for _, want := range []string{
		"<Repetition><Interval>PT5M</Interval><StopAtDurationEnd>false</StopAtDurationEnd></Repetition>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<LogonTrigger>",
		`<Command>C:\Users\sam\AppData\Local\Quietport\qpsync-agent.exe</Command>`,
	} {
		if !strings.Contains(x, want) {
			t.Errorf("the task is missing %s", want)
		}
	}
	// no <Duration>: Task Scheduler reads a repetition without one as "indefinitely", which is what a folder that
	// syncs for years needs
	if strings.Contains(x, "<Duration>") {
		t.Error("the repetition must not end while the person is logged on")
	}
}

// A device that updates itself from 0.1.21 or 0.1.22 keeps the task it was installed with, so the agent looks at the
// registered task when it starts and re-registers only that older shape. schtasks writes its /XML output as UTF-16
// on some Windows versions, so the check has to survive the NUL bytes.
func TestTaskNeedsRefreshOnlyWithoutTheRepetition(t *testing.T) {
	old := `<Triggers><LogonTrigger><Enabled>true</Enabled><UserId>sam</UserId></LogonTrigger></Triggers>`
	if !taskNeedsRefresh(old) {
		t.Error("a task from 0.1.21 has no repetition and has to be re-registered")
	}
	if !taskNeedsRefresh(utf16ish(old)) {
		t.Error("the same task read back as UTF-16 has to be recognised too")
	}
	now := taskXML(`sam`, `C:\x\qpsync-agent.exe`)
	if taskNeedsRefresh(now) || taskNeedsRefresh(utf16ish(now)) {
		t.Error("a task that already repeats must be left alone")
	}
}

func utf16ish(s string) string {
	b := make([]byte, 0, len(s)*2)
	for _, c := range []byte(s) {
		b = append(b, c, 0)
	}
	return string(b)
}
