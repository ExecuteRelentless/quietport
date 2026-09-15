package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"

	"quietport.app/quietport/internal/model"
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

// A heartbeat reported "relayed" whenever it read `tailscale status` while the hub's path sat idle: the daemon clears
// CurAddr a few seconds after the last packet even though the path stays direct, so about 1 heartbeat in 6 across the
// fleet said relayed on direct paths (docs/adr/0024). The path is what a ping to the hub actually took. The outputs
// below are real ones from tailscale 1.102.3 on 2026-09-15 (the timeout lines are that release's own wording).
func TestTheHubPathIsWhatAPingToTheHubTook(t *testing.T) {
	for _, c := range []struct{ name, out, want string }{
		{"direct on the first pong", "pong from quietport-hub (100.64.0.1) via 165.1.66.170:41641 in 54ms\n", "direct"},
		{"relayed on every pong", "pong from quietport-hub (100.64.0.1) via DERP(sfo) in 172ms\npong from quietport-hub (100.64.0.1) via DERP(sfo) in 58ms\npong from quietport-hub (100.64.0.1) via DERP(sfo) in 54ms\n", "relayed"},
		{"relayed first, then direct", "pong from quietport-hub (100.64.0.1) via DERP(sfo) in 61ms\npong from quietport-hub (100.64.0.1) via 165.1.66.170:41641 in 23ms\n", "direct"},
		{"no answer", "ping \"100.64.0.1\" timed out\nping \"100.64.0.1\" timed out\nping \"100.64.0.1\" timed out\n", "down"},
		{"nothing printed", "", "down"},
	} {
		if got := pathFromPing([]byte(c.out)); got != c.want {
			t.Errorf("%s: pathFromPing = %q, want %q", c.name, got, c.want)
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
		// a self-update renames the running daemon's file aside before placing the new one, and Windows reports a
		// running program by its file's current name: the daemon an update left behind runs as ".prev" (Owl, 0.1.25,
		// 2026-09-13), or as a ".prev-<n>" copy moved out of the next update's way
		{PID: 25, Exe: app + `\` + daemonName("windows") + `.prev`},
		{PID: 26, Exe: app + `\` + daemonName("windows") + `.prev-1789400000`},
		{PID: 27, Exe: app + `\qpsync-agent.exe.prev`}, // still an agent
	}
	if got, want := leftoverDaemons(procs, app), []int{21, 22, 25, 26}; !slices.Equal(got, want) {
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

// A device installed on 0.1.21 or 0.1.22 keeps its Scheduled Task through a self-update, which replaces binaries and
// nothing else, so the agent replaces the task itself when it starts. It replaces only a task that is registered: an
// install whose schtasks call failed wrote an HKCU Run key instead, and adding a task to that computer would start a
// second agent at every logon.
func TestTaskIsReplacedOnlyWhenAnOlderOneIsRegistered(t *testing.T) {
	old := `<Triggers><LogonTrigger><Enabled>true</Enabled><UserId>sam</UserId></LogonTrigger></Triggers>`
	if !shouldRefreshTask(old, nil) {
		t.Error("a registered task with no repetition is the one case that gets replaced")
	}
	if shouldRefreshTask(taskXML("sam", `C:\x\qpsync-agent.exe`), nil) {
		t.Error("a task that already repeats must be left alone")
	}
	if shouldRefreshTask("", errors.New("ERROR: The system cannot find the file specified.")) {
		t.Error("no task registered: this install starts the agent from the Run key, and a task would start a second one")
	}
	if shouldRefreshTask(old, errors.New("schtasks is not on this computer")) {
		t.Error("a query that failed says nothing about what is registered")
	}
}

// Install begins from nothing and would replace this computer's device and drop every folder's config. On a
// computer that already has Quietport it refuses, before it reads anything, and points at the join (docs/adr/0019).
// The shell installers and the qpsync-agent command both reach Install; this is the last line behind both.
func TestInstallRefusesToRunOverAnInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	if Installed() {
		t.Fatal("a fresh home must not count as installed")
	}
	err := Install(context.Background(), "abc", filepath.Join(home, "no-such-payload.json"))
	if err == nil || !strings.Contains(err.Error(), "invitation file") {
		t.Fatalf("with nothing installed, Install must get as far as the payload: %v", err)
	}
	_ = os.MkdirAll(AppDir(), 0o700)
	_ = os.WriteFile(ConfigPath(), []byte("{\n  \"version\": 1,\n  \"device_id\": 5,\n  \"socks_port\": 1\n}\n"), 0o600)
	if !Installed() {
		t.Fatal("a config with a device is an install")
	}
	err = Install(context.Background(), "abc", filepath.Join(home, "no-such-payload.json"))
	if err == nil || !strings.Contains(err.Error(), "already on this computer") {
		t.Fatalf("Install must refuse to run over an install: %v", err)
	}
	if b, _ := os.ReadFile(ConfigPath()); !strings.Contains(string(b), `"device_id": 5`) {
		t.Fatal("the refusal touched the config")
	}
}

// A join adds the link's folders to a computer that already has Quietport and touches nothing else on it
// (docs/adr/0019). Install could start from nothing and wipe the folder list; a join starts from a computer with
// folders in it. A link for a folder this computer is already in, but whose key it lost, heals it. A key for a
// generation the folder has moved past is kept for what it is, and the folder is marked as still needing the key.
func TestJoinKeepsTheFoldersAlreadyOnThisComputer(t *testing.T) {
	seal := func(v any) (string, error) { b, _ := json.Marshal(v); return "sealed:" + string(b), nil }
	c := Config{DeviceID: 4, Circles: []CircleState{
		{CircleConfig: model.CircleConfig{ID: 1, Slug: "family", DisplayName: "Family", Generation: 3}, KeySealed: "family-key", KeyGen: 3, S3Sealed: "family-s3"},
		{CircleConfig: model.CircleConfig{ID: 2, Slug: "old", DisplayName: "Old"}, Removed: true},
	}}
	trip := model.CircleConfig{ID: 9, Slug: "trip-9x2a", DisplayName: "Trip", Generation: 1, S3AccessKey: "AK", S3SecretKey: "SK"}
	tripKey := model.CircleKey{Slug: "trip-9x2a", Generation: 1, Password: "p", Salt: "s"}
	if got, _ := joinCircles(&c, []model.CircleConfig{trip}, []model.CircleKey{tripKey}, seal); !slices.Equal(got, []string{"Trip"}) {
		t.Fatalf("joined folders: %v", got)
	}
	if len(c.Circles) != 3 {
		t.Fatalf("expected the two folders already here plus Trip, got %d", len(c.Circles))
	}
	if f := c.Circles[0]; f.KeySealed != "family-key" || f.KeyGen != 3 || f.S3Sealed != "family-s3" || f.Resync || f.NeedsKey {
		t.Errorf("Family was touched: %+v", f)
	}
	if !c.Circles[1].Removed {
		t.Error("the removed folder came back")
	}
	sealedKey, _ := seal(tripKey)
	sealedS3, _ := seal([2]string{"AK", "SK"})
	if f := c.Circles[2]; f.ID != trip.ID || f.Slug != trip.Slug || f.DisplayName != "Trip" || f.Generation != 1 || f.KeySealed != sealedKey || f.KeyGen != 1 || !f.Resync || f.NeedsKey || f.S3Sealed != sealedS3 {
		t.Errorf("Trip: %+v", f)
	}
	// the same link on the same computer again: no duplicate, still three
	joinCircles(&c, []model.CircleConfig{trip}, []model.CircleKey{tripKey}, seal)
	if len(c.Circles) != 3 {
		t.Fatalf("a second join duplicated the folder: %d", len(c.Circles))
	}
	// a folder already here whose key was lost is healed by a link for it
	c.Circles[0].NeedsKey, c.Circles[0].KeySealed, c.Circles[0].KeyGen = true, "", 0
	family := model.CircleConfig{ID: 1, Slug: "family", DisplayName: "Family", Generation: 3, S3AccessKey: "FAK", S3SecretKey: "FSK"}
	if got, _ := joinCircles(&c, []model.CircleConfig{family}, []model.CircleKey{{Slug: "family", Generation: 3, Password: "fp", Salt: "fs"}}, seal); !slices.Equal(got, []string{"Family"}) {
		t.Fatalf("healed folders: %v", got)
	}
	if f := c.Circles[0]; f.NeedsKey || f.KeySealed == "" || f.KeyGen != 3 || !f.Resync {
		t.Errorf("Family was not healed: %+v", f)
	}
	// the same link again on a folder that is healthy: nothing changes, and no resync is scheduled
	c.Circles[2].Resync = false
	joinCircles(&c, []model.CircleConfig{trip}, []model.CircleKey{tripKey}, seal)
	if f := c.Circles[2]; f.Resync || f.NeedsKey || f.KeyGen != 1 {
		t.Errorf("a join that changed nothing must not schedule a resync: %+v", f)
	}
	// a link sealed before the folder was re-keyed: the key is kept for its generation and the folder still needs one
	joinCircles(&c, []model.CircleConfig{{ID: 9, Slug: "trip-9x2a", DisplayName: "Trip", Generation: 2}}, []model.CircleKey{tripKey}, seal)
	if f := c.Circles[2]; !f.NeedsKey || f.KeyGen != 1 || f.Generation != 2 {
		t.Errorf("a stale key must not pass for the current one: %+v", f)
	}
	// a stale link must never replace a newer key this computer already holds: the folder was re-keyed to
	// generation 2 and this computer has that key; a still-live generation-1 link changes nothing
	gen2, _ := seal(model.CircleKey{Slug: "trip-9x2a", Generation: 2, Password: "p2", Salt: "s2"})
	c.Circles[2].KeySealed, c.Circles[2].KeyGen, c.Circles[2].NeedsKey, c.Circles[2].Resync = gen2, 2, false, false
	joinCircles(&c, []model.CircleConfig{{ID: 9, Slug: "trip-9x2a", DisplayName: "Trip", Generation: 2}}, []model.CircleKey{tripKey}, seal)
	if f := c.Circles[2]; f.KeySealed != gen2 || f.KeyGen != 2 || f.NeedsKey || f.Resync {
		t.Errorf("an older key replaced a newer one: %+v", f)
	}
	// a folder whose key did not arrive is listed and marked, not dropped
	c2 := Config{}
	joinCircles(&c2, []model.CircleConfig{trip}, nil, seal)
	if len(c2.Circles) != 1 || !c2.Circles[0].NeedsKey || c2.Circles[0].KeySealed != "" {
		t.Errorf("a folder without its key: %+v", c2.Circles)
	}
}

// A folder's name is typed by whoever started the folder, and every member's computer turns it into a directory
// (docs/adr/0020). A folder called ".." was the member's home directory, synced to everyone in the folder, and "."
// was the sync root with every other folder in it. No name may leave the sync root, be the sync root, hide, or name
// a Windows device; a name that is nothing but such characters becomes "Shared", the name signup gives by default.
func TestAFolderNameNeverLeavesTheSyncRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for name, want := range map[string]string{
		"..":                 "Shared",
		".":                  "Shared",
		"...":                "Shared",
		". .":                "Shared",
		" .. ":               "Shared",
		"":                   "Shared",
		"   ":                "Shared",
		"\x00":               "Shared",
		"../..":              "-",
		`..\..`:              "-",
		"a/../../b":          "a-..-..-b",
		".config":            "config",
		"Photos.":            "Photos",
		"Photos ":            "Photos",
		"x\x01y":             "xy",
		"CON":                "Folder CON",
		"nul":                "Folder nul",
		"Com1":               "Folder Com1",
		"lpt9.txt":           "Folder lpt9.txt",
		"CONOUT$":            "Folder CONOUT$",
		"Console":            "Console",
		"Trip":               "Trip",
		"Mum's photos: 2026": "Mum's photos- 2026",
		"Famille été":        "Famille été",
		// typed with a combining accent, as some keyboards send it: stored in the composed form, so every computer
		// and the hub agree on one name for one directory
		"Cafe\u0301": "Caf\u00e9",
		// cut to 40 characters: a device name with a long extension keeps its prefix, and a long name that is a
		// device name once cut and trimmed gets one
		"lpt9." + strings.Repeat("a", 40):     "Folder lpt9." + strings.Repeat("a", 28),
		"CON" + strings.Repeat(" ", 37) + "x": "Folder CON",
		strings.Repeat("é", 39) + "éé end":    strings.Repeat("é", 40),
	} {
		dir := CircleState{CircleConfig: model.CircleConfig{DisplayName: name}}.Dir()
		if got := filepath.Base(dir); got != want || filepath.Dir(dir) != SyncRoot() {
			t.Errorf("folder %q lands in %s, want %s", name, dir, filepath.Join(SyncRoot(), want))
		}
		// the hub stores the rule's result and every computer applies the rule again: the second pass changes nothing
		if again := safeName(filepath.Base(dir)); again != filepath.Base(dir) {
			t.Errorf("folder %q: %q the first time, %q the second", name, filepath.Base(dir), again)
		}
	}
}

// Two folders with one name never share a directory on a computer (docs/adr/0021). The hub lets a person hold any
// number of folders called "Shared": their own, one a friend started, one the operator made. Each syncs its
// directory with its own bucket, so two of them in one directory would carry one folder's files to the other
// folder's members. The folder already here keeps its directory; the one arriving takes the next free name.
// macOS and Windows do not tell "shared" from "Shared", so neither does this.
func TestTwoFoldersWithOneNameGetTheirOwnDirectories(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seal := func(v any) (string, error) { b, _ := json.Marshal(v); return "sealed:" + string(b), nil }
	c := Config{DeviceID: 4, Circles: []CircleState{
		// written by 0.1.25, which had no directory of its own recorded: the display name was the directory
		{CircleConfig: model.CircleConfig{ID: 1, Slug: "shared-a1", DisplayName: "Shared", Generation: 1}, KeySealed: "k", KeyGen: 1},
		// removed here: its directory no longer belongs to it, so it holds no name
		{CircleConfig: model.CircleConfig{ID: 2, Slug: "shared-b2", DisplayName: "Shared 2", Generation: 1}, KeySealed: "k", KeyGen: 1, Removed: true},
	}}
	friend := model.CircleConfig{ID: 3, Slug: "shared-c3", DisplayName: "Shared", Generation: 1}
	friendKey := model.CircleKey{Slug: "shared-c3", Generation: 1, Password: "p", Salt: "s"}
	names, arrived := joinCircles(&c, []model.CircleConfig{friend}, []model.CircleKey{friendKey}, seal)
	if !slices.Equal(names, []string{"Shared 2"}) || !slices.Equal(arrived, []string{"shared-c3"}) {
		t.Fatalf("joined %v, arrived %v: the member must be told the directory the folder is in", names, arrived)
	}
	if got := c.Circles[0].Dir(); got != filepath.Join(SyncRoot(), "Shared") {
		t.Errorf("the folder already here moved to %s", got)
	}
	if got := c.Circles[2].Dir(); got != filepath.Join(SyncRoot(), "Shared 2") {
		t.Errorf("the arriving folder is in %s", got)
	}
	if c.Circles[0].Resync {
		t.Error("the folder already here did not move, so it must not resync")
	}
	// a third, in lower case, from someone else
	names, _ = joinCircles(&c, []model.CircleConfig{{ID: 4, Slug: "shared-d4", DisplayName: "shared", Generation: 1}}, nil, seal)
	if !slices.Equal(names, []string{"shared 3"}) {
		t.Fatalf("a name that differs only in case must not share a directory: %v", names)
	}
	// one name in two Unicode forms is one directory on macOS, so it is one name here
	names, _ = joinCircles(&c, []model.CircleConfig{{ID: 5, Slug: "cafe-e5", DisplayName: "Caf\u00e9", Generation: 1}}, nil, seal)
	if again, _ := joinCircles(&c, []model.CircleConfig{{ID: 6, Slug: "cafe-f6", DisplayName: "Cafe\u0301", Generation: 1}}, nil, seal); !slices.Equal(names, []string{"Caf\u00e9"}) || !slices.Equal(again, []string{"Caf\u00e9 2"}) {
		t.Fatalf("one name in two Unicode forms: %q then %q", names, again)
	}
	// set-aside files go to "Previous files <date>" in the sync root, which syncs nowhere; a folder never has that name
	if names, _ = joinCircles(&c, []model.CircleConfig{{ID: 7, Slug: "prev-g7", DisplayName: "previous files 2026-09-14", Generation: 1}}, nil, seal); !slices.Equal(names, []string{"Folder previous files 2026-09-14"}) {
		t.Fatalf("a folder named like the set-aside place: %q", names)
	}
	// the same link again: the folder keeps the directory it was given
	names, arrived = joinCircles(&c, []model.CircleConfig{friend}, []model.CircleKey{friendKey}, seal)
	if !slices.Equal(names, []string{"Shared 2"}) || len(arrived) != 0 {
		t.Fatalf("a second join of the same folder: joined %v, arrived %v", names, arrived)
	}
	dirs := map[string]string{}
	for _, cs := range c.Circles {
		if cs.Removed {
			continue
		}
		key := strings.ToLower(norm.NFC.String(cs.Dir()))
		if other, ok := dirs[key]; ok {
			t.Fatalf("%s and %s share %s", other, cs.Slug, cs.Dir())
		}
		dirs[key] = cs.Slug
	}
}

// A folder that arrives on a computer starts with nothing in its directory (docs/adr/0021, extending 0016 from
// installs to every arrival). Its first sync is a full resync, which sends up whatever the directory holds, and a
// directory of that name can hold anything: a removed folder's files left in place, another person's leftovers, or a
// member's own directory that happens to have the name. What was there goes to "Previous files <date>", which
// syncs nowhere. A folder that was already here is its own synced copy and is never touched. Setting aside twice in
// one day keeps both copies. A set-aside that fails leaves the folder unsynced until one succeeds: a half-emptied
// directory would send the other half up.
func TestAFolderArrivesWithNothingInIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := SyncRoot()
	write := func(p, body string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "Trip", "budget.xlsx"), "left over")
	write(filepath.Join(root, "Trip", MarkerFile), "marker")
	write(filepath.Join(root, "Family", "photo.jpg"), "synced")
	seal := func(v any) (string, error) { b, _ := json.Marshal(v); return "sealed:" + string(b), nil }
	c := Config{DeviceID: 4, Circles: []CircleState{
		{CircleConfig: model.CircleConfig{ID: 1, Slug: "family", DisplayName: "Family", Generation: 1}, KeySealed: "k", KeyGen: 1, Folder: "Family"},
	}}
	trip := model.CircleConfig{ID: 9, Slug: "trip-9x2a", DisplayName: "Trip", Generation: 1}
	family := model.CircleConfig{ID: 1, Slug: "family", DisplayName: "Family", Generation: 1}
	tripKey := model.CircleKey{Slug: "trip-9x2a", Generation: 1, Password: "p", Salt: "s"}
	_, fresh := joinCircles(&c, []model.CircleConfig{trip, family}, []model.CircleKey{tripKey}, seal)
	if !slices.Equal(fresh, []string{"trip-9x2a"}) {
		t.Fatalf("new here: %v, want only the folder that arrived", fresh)
	}
	if n, err := emptyNewFolders(&c, fresh, "2026-09-14"); n != 1 || err != nil {
		t.Fatalf("moved %d entries (err %v), want the one left-over file", n, err)
	}
	aside := filepath.Join(root, "Previous files 2026-09-14", "Trip")
	if b, err := os.ReadFile(filepath.Join(aside, "budget.xlsx")); err != nil || string(b) != "left over" {
		t.Fatalf("the left-over file is not in %s: %v", aside, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Trip", "budget.xlsx")); !os.IsNotExist(err) {
		t.Fatal("the left-over file is still in the folder about to sync")
	}
	if _, err := os.Stat(filepath.Join(root, "Family", "photo.jpg")); err != nil {
		t.Fatalf("the folder already here lost its files: %v", err)
	}
	if !c.Circles[1].syncable() || c.Circles[1].AsidePending {
		t.Fatalf("an emptied folder with its key must sync: %+v", c.Circles[1])
	}
	// the folder is removed and comes back the same day, and its directory has gathered a file again
	write(filepath.Join(root, "Trip", "budget.xlsx"), "second")
	c.Circles[1].Removed = true
	_, fresh = joinCircles(&c, []model.CircleConfig{trip}, []model.CircleKey{tripKey}, seal)
	if n, err := emptyNewFolders(&c, fresh, "2026-09-14"); n != 1 || err != nil {
		t.Fatalf("second arrival moved %d (err %v)", n, err)
	}
	for dir, want := range map[string]string{aside: "left over", aside + " 2": "second"} {
		if b, err := os.ReadFile(filepath.Join(dir, "budget.xlsx")); err != nil || string(b) != want {
			t.Errorf("%s: %q, %v; want %q: a second set-aside must not overwrite the first", dir, b, err, want)
		}
	}
	// the next day the set-aside place cannot be made: the folder keeps its files and does not sync
	write(filepath.Join(root, "Trip", "budget.xlsx"), "third")
	write(filepath.Join(root, "Previous files 2026-09-15"), "a file where the directory should go")
	c.Circles[1].Removed = true
	_, fresh = joinCircles(&c, []model.CircleConfig{trip}, []model.CircleKey{tripKey}, seal)
	if _, err := emptyNewFolders(&c, fresh, "2026-09-15"); err == nil {
		t.Fatal("a set-aside that could not happen reported success")
	}
	if f := c.Circles[1]; !f.AsidePending || f.syncable() {
		t.Fatalf("a folder whose directory could not be emptied must wait: %+v", f)
	}
	_ = os.Remove(filepath.Join(root, "Previous files 2026-09-15"))
	if n, err := emptyNewFolders(&c, []string{"trip-9x2a"}, "2026-09-15"); n != 1 || err != nil || c.Circles[1].AsidePending || !c.Circles[1].syncable() {
		t.Fatalf("the retry: moved %d, %v, %+v", n, err, c.Circles[1])
	}
}

// A computer that 0.1.25 left with two live folders of one name, both syncing one directory: the later moves to its
// own, and that directory counts as new to it, so it is emptied before the folder syncs there (docs/adr/0021).
func TestAFolderMovedOffASharedDirectoryIsEmptiedLikeAnArrival(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seal := func(v any) (string, error) { return "sealed", nil }
	c := Config{DeviceID: 4, Circles: []CircleState{
		{CircleConfig: model.CircleConfig{ID: 1, Slug: "photos-a", DisplayName: "Photos", Generation: 1}, KeySealed: "k", KeyGen: 1},
		{CircleConfig: model.CircleConfig{ID: 2, Slug: "photos-b", DisplayName: "Photos", Generation: 1}, KeySealed: "k", KeyGen: 1},
	}}
	_, fresh := joinCircles(&c, []model.CircleConfig{{ID: 3, Slug: "trip", DisplayName: "Trip", Generation: 1}}, nil, seal)
	if !slices.Equal(fresh, []string{"photos-b", "trip"}) {
		t.Fatalf("new here: %v, want the folder that moved and the one that arrived", fresh)
	}
	if c.Circles[0].Folder != "Photos" || c.Circles[0].Resync || c.Circles[1].Folder != "Photos 2" || !c.Circles[1].Resync {
		t.Fatalf("placement: %+v", c.Circles[:2])
	}
}

// A folder renamed on the hub takes its new name on this computer only where that name is free (docs/adr/0021).
// Before 0.1.26 a rename whose name was already a directory here left the files where they were but pointed the
// folder at the other directory, which then synced with this folder's bucket: another folder's files, or anything
// else of that name, went to this folder's members.
func TestARenamedFolderNeverMovesIntoADirectoryThatIsTaken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := SyncRoot()
	a := &Agent{logger: log.New(io.Discard, "", 0)}
	c := Config{Circles: []CircleState{
		{CircleConfig: model.CircleConfig{Slug: "trip", DisplayName: "Trip"}, Folder: "Trip"},
		{CircleConfig: model.CircleConfig{Slug: "family", DisplayName: "Family"}, Folder: "Family"},
	}}
	for p, body := range map[string]string{"Trip/plan.txt": "trip", "Family/photo.jpg": "family", "Holiday/notes.txt": "mine"} {
		_ = os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		if err := os.WriteFile(filepath.Join(root, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a.renameFolder(&c, 0, "Holiday")
	if f := c.Circles[0]; f.Folder != "Trip" || f.Resync {
		t.Errorf("renamed to a name already on disk: folder %q, resync %v; it must stay in Trip", f.Folder, f.Resync)
	}
	if _, err := os.Stat(filepath.Join(root, "Trip", "plan.txt")); err != nil {
		t.Errorf("the folder's files moved: %v", err)
	}
	a.renameFolder(&c, 0, "family")
	if f := c.Circles[0]; f.Folder != "family 2" || !f.Resync {
		t.Errorf("renamed to another folder's name: folder %q, resync %v", f.Folder, f.Resync)
	}
	if b, err := os.ReadFile(filepath.Join(root, "family 2", "plan.txt")); err != nil || string(b) != "trip" {
		t.Errorf("the files did not move with the folder: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "Family")); len(entries) != 1 {
		t.Errorf("the other folder's directory changed: %d entries", len(entries))
	}
	a.renameFolder(&c, 0, "Summer")
	if b, err := os.ReadFile(filepath.Join(root, "Summer", "plan.txt")); err != nil || string(b) != "trip" || c.Circles[0].Folder != "Summer" {
		t.Errorf("a free name: folder %q, %v", c.Circles[0].Folder, err)
	}
}

// Watches follow the folders that are live here (docs/adr/0021). A removed folder keeps the directory it had, and a
// live folder can have taken that directory since; stopping the removed one's watch must not stop the live one's.
// Stopping a watch on ".../Shared" must not stop ".../Shared 2" either: a changed file there would then wait for the
// next scheduled sync instead of syncing when it changes.
func TestStoppingOneFoldersWatchLeavesTheOthers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	circles := []CircleState{
		{CircleConfig: model.CircleConfig{Slug: "old", DisplayName: "Shared"}, Folder: "Shared", Removed: true},
		{CircleConfig: model.CircleConfig{Slug: "new", DisplayName: "Shared"}, Folder: "Shared"},
		{CircleConfig: model.CircleConfig{Slug: "two", DisplayName: "Shared"}, Folder: "Shared 2"},
		{CircleConfig: model.CircleConfig{Slug: "gone", DisplayName: "Trip"}, Folder: "Trip", Removed: true},
	}
	watch, stop := watchRoots(circles)
	if !slices.Equal(stop, []string{filepath.Join(SyncRoot(), "Trip")}) {
		t.Errorf("stop watching %v, want only Trip: Shared belongs to a live folder now", stop)
	}
	if len(watch) != 2 || watch[filepath.Join(SyncRoot(), "Shared")] != "new" || watch[filepath.Join(SyncRoot(), "Shared 2")] != "two" {
		t.Errorf("watch %v", watch)
	}

	w, err := NewWatcher(time.Hour)
	if err != nil {
		t.Skip("no file watcher here:", err)
	}
	shared, shared2 := filepath.Join(SyncRoot(), "Shared"), filepath.Join(SyncRoot(), "Shared 2")
	for _, d := range []string{shared, shared2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w.AddRoot("a", shared)
	w.AddRoot("b", shared2)
	w.RemoveRoot(shared)
	if !slices.Contains(w.w.WatchList(), shared2) || w.slugFor(filepath.Join(shared2, "x.txt")) != "b" {
		t.Errorf("stopping Shared stopped Shared 2: %v", w.w.WatchList())
	}
}

// Every device writes the same marker, byte for byte and to the second (docs/adr/0022). Each device used to stamp
// its marker with the moment it wrote it. In a folder holding nothing else, the second member's first full sync then
// replaced the marker on the hub, and the first member's device saw the folder's only file changed: rclone refuses a
// bisync in which every file changed, so that device stopped syncing the folder. A marker already in a folder is left
// as it is: it is the copy the hub has, and changing it is a change bisync counts like any other.
func TestEveryDeviceWritesTheSameMarker(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	ensureMarker(first)
	time.Sleep(20 * time.Millisecond)
	ensureMarker(second)
	a, errA := os.Stat(filepath.Join(first, MarkerFile))
	b, errB := os.Stat(filepath.Join(second, MarkerFile))
	if errA != nil || errB != nil {
		t.Fatalf("markers not written: %v %v", errA, errB)
	}
	if !a.ModTime().Equal(b.ModTime()) || a.Size() != b.Size() {
		t.Fatalf("two devices wrote different markers: %v %d and %v %d", a.ModTime(), a.Size(), b.ModTime(), b.Size())
	}
	synced := time.Date(2026, 9, 13, 4, 36, 41, 0, time.UTC)
	_ = os.Chtimes(filepath.Join(second, MarkerFile), synced, synced)
	ensureMarker(second)
	if st, _ := os.Stat(filepath.Join(second, MarkerFile)); !st.ModTime().Equal(synced) {
		t.Errorf("a marker already in the folder was re-stamped: %v", st.ModTime())
	}
}

func markerListings(t *testing.T, names ...string) string {
	t.Helper()
	work := t.TempDir()
	lst := "# bisync listing v1 from 2026-09-13T04:37:18.000000000+0000\n"
	for _, name := range names {
		lst += fmt.Sprintf("-       83 - - 2026-09-13T04:36:41.000000000+0000 %q\n", name)
	}
	for _, side := range []string{"path1", "path2"} {
		if err := os.WriteFile(filepath.Join(work, "Users_pat_QPSync_Shared..QPCRYPT_."+side+".lst"), []byte(lst), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return work
}

const refusedOverEveryFile = "2026/09/13 22:09:04 ERROR : Safety abort: all files were changed on Path2 \"QPCRYPT:\". Run with --force if desired.\n" +
	"2026/09/13 22:09:04 NOTICE: Bisync aborted. Please try again.\n2026/09/13 22:09:04 NOTICE: Failed to bisync: all files were changed\n"

// A refusal because every file changed is about the marker alone only when every listing bisync kept from its last
// good run knew of nothing but the marker (docs/adr/0022). With any file in the last listing the refusal stands: it is
// rclone's guard against a folder whose every file was replaced.
func TestARefusalIsOverTheMarkerAloneOnlyWhenTheLastSyncKnewNothingElse(t *testing.T) {
	if !refusalOverTheMarkerAlone(refusedOverEveryFile, markerListings(t, MarkerFile)) {
		t.Error("refused, and the last listing knew only the marker")
	}
	if refusalOverTheMarkerAlone(refusedOverEveryFile, markerListings(t, MarkerFile, "budget.xlsx")) {
		t.Error("refused with a file in the last listing: the guard stands")
	}
	if refusalOverTheMarkerAlone("2026/09/13 NOTICE: Failed to bisync: empty prior Path1 listing\n", markerListings(t, MarkerFile)) {
		t.Error("another failure is not this one")
	}
	stale := markerListings(t, MarkerFile)
	_ = os.WriteFile(filepath.Join(stale, "Users_pat_QPSync_Old..QPCRYPT_.path2.lst"), []byte("# bisync listing v1\n-       6 - - 2026-09-01T00:00:00.000000000+0000 \"notes.txt\"\n"), 0o600)
	if refusalOverTheMarkerAlone(refusedOverEveryFile, stale) {
		t.Error("any listing kept for the folder that knows of a file rules it out")
	}
	if refusalOverTheMarkerAlone(refusedOverEveryFile, t.TempDir()) {
		t.Error("no listing at all rules it out")
	}
}

// A bisync refused over the marker alone takes a full sync in the same cycle, and that full sync keeps the hub's copy
// where the two differ (docs/adr/0022). So a device never pushes its own marker at the others: the hub's marker
// only changes when a folder's first full sync on a device, or a 0.1.25 device, puts one there, and every 0.1.26
// device simply takes it. The cycle's result is the full sync's, so a healed folder is not a failure.
func TestARefusalOverTheMarkerAloneIsSettledInTheSameCycleKeepingTheHubsCopy(t *testing.T) {
	type call struct{ resync string }
	runner := func(results ...Result) (func(string) Result, *[]call) {
		var calls []call
		return func(resync string) Result {
			calls = append(calls, call{resync})
			r := results[0]
			results = results[1:]
			return r
		}, &calls
	}
	refused, synced := Result{Output: refusedOverEveryFile}, Result{OK: true}

	run, calls := runner(refused, synced)
	if res, healed := bisyncHealingTheMarker(run, noResync, markerListings(t, MarkerFile)); !res.OK || !healed ||
		!slices.Equal(*calls, []call{{noResync}, {resyncKeepHub}}) {
		t.Errorf("refused over the marker: ok %v, healed %v, calls %v", res.OK, healed, *calls)
	}
	run, calls = runner(refused)
	if res, healed := bisyncHealingTheMarker(run, noResync, markerListings(t, MarkerFile, "budget.xlsx")); res.OK || healed || len(*calls) != 1 {
		t.Errorf("refused with a file in the listing: ok %v, healed %v, calls %v", res.OK, healed, *calls)
	}
	run, calls = runner(refused)
	if _, healed := bisyncHealingTheMarker(run, resyncKeepNewer, markerListings(t, MarkerFile)); healed || !slices.Equal(*calls, []call{{resyncKeepNewer}}) {
		t.Errorf("a cycle that is already a full sync: healed %v, calls %v", healed, *calls)
	}
	run, calls = runner(synced)
	if res, healed := bisyncHealingTheMarker(run, noResync, markerListings(t, MarkerFile)); !res.OK || healed || len(*calls) != 1 {
		t.Errorf("a cycle that synced: ok %v, healed %v, calls %v", res.OK, healed, *calls)
	}
}

// Two devices share a folder through rclone itself (docs/adr/0022). Set QP_RCLONE to an rclone 1.75 binary to run
// it; CI downloads one. The hub's bucket is stood in for by a local directory, which keeps modification times as the
// crypt remote does. A 0.1.26 cycle is the agent's own bisyncHealingTheMarker over its own bisyncArgs. A 0.1.25 cycle
// stamps its marker when it writes it and, after three refusals, takes a full sync that keeps its own copy.
func TestTwoDevicesSyncASharedFolderThroughRclone(t *testing.T) {
	bin := os.Getenv("QP_RCLONE")
	if bin == "" {
		t.Skip("set QP_RCLONE to an rclone binary to run the two-device sync")
	}
	t.Setenv("HOME", t.TempDir())
	type device struct {
		name, dir, work string
		legacy          bool      // runs 0.1.25
		stamp           time.Time // the time a 0.1.25 device writes on its marker
		full            bool      // the next cycle is a full sync
		refusals        int       // consecutive failures, for 0.1.25's three-refusal rule
	}
	var hub string
	pair := func() (pat, sam *device) {
		// bisync names its listing files after both paths, so the paths stay short or the names pass the limit
		base, err := os.MkdirTemp(filepath.Join(string(os.PathSeparator), "tmp"), "qp")
		if runtime.GOOS == "windows" || err != nil {
			base, err = os.MkdirTemp("", "qp")
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(base) })
		hub = filepath.Join(base, "bucket") + string(os.PathSeparator)
		_ = os.MkdirAll(hub, 0o755)
		mk := func(name string, stamp time.Time) *device {
			d := &device{name: name, dir: filepath.Join(base, name, "Shared"), work: filepath.Join(base, name, "bisync"), stamp: stamp, full: true}
			_ = os.MkdirAll(d.dir, 0o755)
			_ = os.MkdirAll(d.work, 0o700)
			return d
		}
		return mk("pat", time.Now().Add(-time.Hour)), mk("sam", time.Now())
	}
	rclone := func(d *device, resync string) Result {
		cmd := exec.Command(bin, bisyncArgs(CircleState{}, d.dir, hub, d.work, resync)...)
		cmd.Env = append(os.Environ(), "RCLONE_CONFIG="+os.DevNull)
		out, err := cmd.CombinedOutput()
		return Result{OK: err == nil, Err: err, Output: string(out)}
	}
	cycle := func(d *device) (res Result, healed bool) {
		if d.legacy {
			p := filepath.Join(d.dir, MarkerFile)
			if _, err := os.Stat(p); os.IsNotExist(err) {
				_ = os.WriteFile(p, []byte(markerText), 0o644)
				_ = os.Chtimes(p, d.stamp, d.stamp)
			}
			resync := noResync
			if d.full || d.refusals >= 3 {
				resync = resyncThisDevice
			}
			res = rclone(d, resync)
		} else {
			ensureMarker(d.dir)
			mode := noResync
			if d.full {
				mode = resyncThisDevice
			}
			res, healed = bisyncHealingTheMarker(func(resync string) Result { return rclone(d, resync) }, mode, d.work)
		}
		if res.OK {
			d.full, d.refusals = false, 0
		} else {
			d.refusals++
		}
		return res, healed
	}
	mustSync := func(d *device, why string) {
		t.Helper()
		if res, _ := cycle(d); !res.OK {
			t.Fatalf("%s: %s did not sync: %v\n%s", why, d.name, res.Err, res.Output)
		}
	}
	// both devices keep taking turns until neither is refused for three rounds in a row; a pair that cannot get
	// there within the rounds given is the loop this test exists for
	settle := func(pat, sam *device, rounds int, why string) {
		t.Helper()
		clean := 0
		for i := 0; i < rounds && clean < 3; i++ {
			a, _ := cycle(pat)
			b, _ := cycle(sam)
			if a.OK && b.OK {
				clean++
			} else {
				clean = 0
			}
		}
		if clean < 3 {
			t.Fatalf("%s: the two devices never settled in %d rounds", why, rounds)
		}
	}
	has := func(d *device, name, want string) {
		t.Helper()
		if b, err := os.ReadFile(filepath.Join(d.dir, name)); err != nil || string(b) != want {
			t.Fatalf("%s: %s is %q, %v; want %q", d.name, name, b, err, want)
		}
	}
	stuckPair := func() (pat, sam *device) {
		pat, sam = pair()
		pat.legacy, sam.legacy = true, true
		mustSync(pat, "Pat starts the folder")
		mustSync(sam, "Sam joins")
		if res, _ := cycle(pat); res.OK || !strings.Contains(res.Output, "all files were changed") {
			t.Fatalf("0.1.25 should leave Pat stuck, got %v\n%s", res.Err, res.Output)
		}
		return pat, sam
	}

	// shared on 0.1.26: nobody is refused and nothing needs healing
	pat, sam := pair()
	mustSync(pat, "Pat starts the folder")
	time.Sleep(1100 * time.Millisecond) // written a second later, as a second device's marker always is
	mustSync(sam, "Sam joins")
	for i := 0; i < 2; i++ {
		for _, d := range []*device{pat, sam} {
			if res, healed := cycle(d); !res.OK || healed {
				t.Fatalf("after the join: %s ok %v, healed %v\n%s", d.name, res.OK, healed, res.Output)
			}
		}
	}
	_ = os.WriteFile(filepath.Join(sam.dir, "plan.txt"), []byte("from sam"), 0o644)
	mustSync(sam, "Sam adds a file")
	mustSync(pat, "Pat picks it up")
	has(pat, "plan.txt", "from sam")

	// a 0.1.25 device's folder, joined from 0.1.26
	pat, sam = pair()
	pat.legacy = true
	mustSync(pat, "Pat starts the folder on 0.1.25")
	mustSync(sam, "Sam joins on 0.1.26")
	settle(pat, sam, 12, "a 0.1.25 folder joined from 0.1.26")

	// stuck on 0.1.25 with nothing in the folder, then both update
	pat, sam = stuckPair()
	pat.legacy, sam.legacy = false, false
	settle(pat, sam, 4, "stuck and empty, both on 0.1.26")

	// stuck on 0.1.25, the stuck device's member adds a file, then both update
	pat, sam = stuckPair()
	_ = os.WriteFile(filepath.Join(pat.dir, "added while stuck.txt"), []byte("from pat"), 0o644)
	pat.legacy, sam.legacy = false, false
	settle(pat, sam, 4, "stuck with a file, both on 0.1.26")
	has(sam, "added while stuck.txt", "from pat")

	// stuck on 0.1.25; only the device that is not stuck updates: it must not keep the other one stuck
	pat, sam = stuckPair()
	sam.legacy = false
	settle(pat, sam, 12, "only the device holding the newer marker updated")

	// syncing on 0.1.25 with one file; Pat's member deletes it, then both update: the delete reaches Sam
	pat, sam = pair()
	pat.legacy, sam.legacy = true, true
	mustSync(pat, "Pat starts the folder")
	_ = os.WriteFile(filepath.Join(pat.dir, "last file.txt"), []byte("x"), 0o644)
	mustSync(pat, "Pat adds a file")
	mustSync(sam, "Sam joins")
	mustSync(pat, "Pat after the join")
	has(sam, "last file.txt", "x")
	_ = os.Remove(filepath.Join(pat.dir, "last file.txt"))
	pat.legacy, sam.legacy = false, false
	settle(pat, sam, 4, "a delete just before the update")
	for _, d := range []*device{pat, sam} {
		if _, err := os.Stat(filepath.Join(d.dir, "last file.txt")); !os.IsNotExist(err) {
			t.Errorf("%s: the deleted file came back", d.name)
		}
	}
}

// A bisync that fails is forced into a full sync only when rclone's saved listings cannot be used (docs/adr/0023):
// it says "must run --resync", or it cannot find them at all, which 1.75.1 calls retryable although no later run
// finds them either. A run rclone's own guard refused ("Safety abort") that is refused three times in a row marks the
// folder refused and is reported once, and the files are left for a person. Any other failure three times in a row,
// such as a device that is offline, is reported once as failing and heals by itself when it can.
func TestAFailedSyncIsForcedOnlyWhenRcloneCannotUseItsListings(t *testing.T) {
	unusable := "2026/09/14 ERROR : Bisync critical error: cannot read prior listing: open /x.path1.lst: bad listing\n" +
		"2026/09/14 ERROR : Bisync aborted. Must run --resync to recover.\n"
	lost := "2026/09/14 10:30:34 ERROR : Bisync critical error: cannot find prior Path1 or Path2 listings, likely due to critical error on prior run \n" +
		"Tip: here are the filenames we were looking for. Do they exist? \n" +
		"2026/09/14 10:30:34 ERROR : Bisync aborted. Error is retryable without --resync due to --resilient mode.\n" +
		"2026/09/14 10:30:34 NOTICE: Failed to bisync: bisync aborted\n"
	offline := "2026/09/14 ERROR : Bisync critical error: dial tcp 100.64.0.1:3900: connect: no route to host\n" +
		"2026/09/14 ERROR : Bisync aborted. Error is retryable without --resync due to --resilient mode.\n"
	refused := "2026/09/14 ERROR : Safety abort: all files were changed on Path1 \"/Users/pat/QPSync/Trip/\". Run with --force if desired.\n" +
		"2026/09/14 NOTICE: Bisync aborted. Please try again.\n2026/09/14 NOTICE: Failed to bisync: all files were changed\n"
	for _, c := range []struct {
		why           string
		output        string
		resync        bool
		failures      int
		full, refused bool
		report        string
	}{
		{"rclone says its listings are unusable", unusable, false, 1, true, false, ""},
		{"rclone cannot find its listings", lost, false, 1, true, false, ""},
		{"unusable, but this cycle already was a full sync, the third time", unusable, true, 3, false, false, "sync_failing"},
		{"offline, the first time", offline, false, 1, false, false, ""},
		{"offline, the third time in a row", offline, false, 3, false, false, "sync_failing"},
		{"offline, the fourth time: already reported", offline, false, 4, false, false, ""},
		{"rclone's guard refused the run, twice", refused, false, 2, false, false, ""},
		{"rclone's guard refused the run, three times", refused, false, 3, false, true, "sync_refused"},
		{"rclone's guard refused the run, ten times: still refused, already reported", refused, false, 10, false, true, ""},
	} {
		full, isRefused, report := afterFailedBisync(c.output, c.resync, c.failures)
		if full != c.full || isRefused != c.refused || report != c.report {
			t.Errorf("%s: full sync %v, refused %v, report %q; want %v, %v, %q", c.why, full, isRefused, report, c.full, c.refused, c.report)
		}
	}
}

// While a folder is refused, no full sync runs that a person did not choose (docs/adr/0023). A full sync the agent
// schedules itself, for a rename, a new key generation or lost listings, keeps this device's copies, which is exactly
// what the refusal is protecting the other members from. It waits, and a person's choice runs.
func TestARefusedFolderTakesOnlyTheFullSyncAPersonChose(t *testing.T) {
	for _, c := range []struct {
		why  string
		cs   CircleState
		want string
	}{
		{"nothing pending", CircleState{}, noResync},
		{"a scheduled full sync", CircleState{Resync: true}, resyncThisDevice},
		{"a scheduled full sync while refused", CircleState{Resync: true, Refused: true}, noResync},
		{"a person chose the hub's copies while refused", CircleState{Resync: true, Refused: true, ResyncKeep: "hub"}, resyncKeepHub},
		{"a person chose this device's copies", CircleState{Resync: true, ResyncKeep: "this"}, resyncThisDevice},
		{"a person chose the newer copies", CircleState{Resync: true, ResyncKeep: "newer"}, resyncKeepNewer},
	} {
		if got := c.cs.resyncMode(); got != c.want {
			t.Errorf("%s: %q, want %q", c.why, got, c.want)
		}
	}
}

// What a forced full sync did to a member's work, through rclone itself (docs/adr/0023). Pat restores a copy of the
// folder from a backup, and every file comes back with a new time. Sam has meanwhile edited one of them. rclone
// refuses Pat's bisync because every file changed. Under 0.1.25 the third refusal forced a full sync keeping Pat's
// copy, and Sam's edit was replaced on the hub. Now the refusals are reported and the hub keeps Sam's edit.
func TestAFolderRefusedThreeTimesIsReportedNotForced(t *testing.T) {
	bin := os.Getenv("QP_RCLONE")
	if bin == "" {
		t.Skip("set QP_RCLONE to an rclone binary to run it")
	}
	base, err := os.MkdirTemp(filepath.Join(string(os.PathSeparator), "tmp"), "qp")
	if runtime.GOOS == "windows" || err != nil {
		base, err = os.MkdirTemp("", "qp")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	hub := filepath.Join(base, "bucket") + string(os.PathSeparator)
	pat, sam := filepath.Join(base, "pat", "Trip"), filepath.Join(base, "sam", "Trip")
	patWork, samWork := filepath.Join(base, "pat", "bisync"), filepath.Join(base, "sam", "bisync")
	for _, d := range []string{hub, pat, sam, patWork, samWork} {
		_ = os.MkdirAll(d, 0o755)
	}
	run := func(dir, work, resync string) Result {
		cmd := exec.Command(bin, bisyncArgs(CircleState{}, dir, hub, work, resync)...)
		cmd.Env = append(os.Environ(), "RCLONE_CONFIG="+os.DevNull)
		out, err := cmd.CombinedOutput()
		return Result{OK: err == nil, Err: err, Output: string(out)}
	}
	must := func(r Result, why string) {
		t.Helper()
		if !r.OK {
			t.Fatalf("%s: %v\n%s", why, r.Err, r.Output)
		}
	}
	week := time.Now().Add(-7 * 24 * time.Hour)
	for _, f := range []string{"plan.txt", "budget.txt"} {
		_ = os.WriteFile(filepath.Join(pat, f), []byte("first draft of "+f), 0o644)
		_ = os.Chtimes(filepath.Join(pat, f), week, week)
	}
	ensureMarker(pat)
	must(run(pat, patWork, resyncThisDevice), "Pat starts the folder")
	ensureMarker(sam)
	must(run(sam, samWork, resyncThisDevice), "Sam joins")
	// Pat restores last week's copy, marker included, stamped now; Sam edits plan.txt, and that reaches the hub
	restored := time.Now().Add(-time.Minute)
	for _, f := range []string{"plan.txt", "budget.txt", MarkerFile} {
		_ = os.Chtimes(filepath.Join(pat, f), restored, restored)
	}
	_ = os.WriteFile(filepath.Join(sam, "plan.txt"), []byte("sam's edit"), 0o644)
	must(run(sam, samWork, noResync), "Sam's edit goes up")

	// Pat's device, cycle by cycle, as syncCircle runs it
	trip := CircleState{CircleConfig: model.CircleConfig{Slug: "trip-x2a4", DisplayName: "Trip"}, Folder: "Trip"}
	failures, reports := 0, []string{}
	cycle := func(why string) Result {
		res := run(pat, patWork, trip.resyncMode())
		if res.OK {
			failures, trip.Resync, trip.ResyncKeep, trip.Refused = 0, false, "", false
			return res
		}
		failures++
		full, refused, report := afterFailedBisync(res.Output, trip.Resync, failures)
		trip.Refused = trip.Refused || refused
		if full {
			trip.Resync = true
		}
		if report != "" {
			reports = append(reports, report)
		}
		return res
	}
	for i := 0; i < 5; i++ {
		cycle("after the restore")
	}
	if !trip.Refused || !slices.Equal(reports, []string{"sync_refused"}) {
		t.Errorf("refused five times: refused %v, reports %v; want refused, reported once", trip.Refused, reports)
	}
	// the folder is renamed on the hub while refused: the agent schedules its own full sync, which must wait
	trip.Resync = true
	cycle("a rename's full sync while refused")
	if b, _ := os.ReadFile(filepath.Join(hub, "plan.txt")); string(b) != "sam's edit" {
		t.Fatalf("the hub's plan.txt is %q: a full sync nobody chose replaced Sam's edit", b)
	}
	// the operator looks, and asks Pat's device for one full sync that keeps the hub's copies
	c := Config{Circles: []CircleState{trip}}
	if _, _, err := c.requestFullSync("trip", "hub"); err != nil {
		t.Fatal(err)
	}
	trip = c.Circles[0]
	if res := cycle("the full sync the operator asked for"); !res.OK {
		t.Fatalf("the full sync the operator asked for: %v\n%s", res.Err, res.Output)
	}
	if res := cycle("Pat's next cycle"); !res.OK || trip.Refused {
		t.Fatalf("Pat's next cycle: %v, refused %v\n%s", res.Err, trip.Refused, res.Output)
	}
	for where, dir := range map[string]string{"the hub": hub, "Pat's folder": pat} {
		if b, _ := os.ReadFile(filepath.Join(dir, "plan.txt")); string(b) != "sam's edit" {
			t.Errorf("%s: plan.txt is %q after keeping the hub's copies", where, b)
		}
	}

	// a crash loses Pat's listings and their backups: rclone never finds them, so the agent forces the full sync
	lists, _ := filepath.Glob(filepath.Join(patWork, "*.lst*"))
	for _, l := range lists {
		_ = os.Remove(l)
	}
	if res := cycle("lost listings"); res.OK || !trip.Resync {
		t.Fatalf("lost listings: ok %v, full sync scheduled %v\n%s", res.OK, trip.Resync, res.Output)
	}
	if res := cycle("the forced full sync"); !res.OK {
		t.Fatalf("the forced full sync: %v\n%s", res.Err, res.Output)
	}
	if res := cycle("healed"); !res.OK {
		t.Fatalf("after the forced full sync: %v\n%s", res.Err, res.Output)
	}
}

// When a folder keeps being refused, a person decides how it recovers (docs/adr/0023): one full sync on that device
// that keeps this device's copies, the hub's, or the newer of each, where the two sides differ. The request names the
// folder as the member sees it, or the circle's slug. It is kept until a sync succeeds.
func TestAFullSyncIsAskedForByFolderWithTheCopyToKeep(t *testing.T) {
	c := Config{Circles: []CircleState{
		{CircleConfig: model.CircleConfig{Slug: "shared-a1", DisplayName: "Shared"}, Folder: "Shared"},
		{CircleConfig: model.CircleConfig{Slug: "shared-b2", DisplayName: "Shared"}, Folder: "Shared 2"},
		{CircleConfig: model.CircleConfig{Slug: "old", DisplayName: "Old"}, Folder: "Old", Removed: true},
	}}
	if name, slug, err := c.requestFullSync("shared 2", "newer"); err != nil || name != "Shared 2" || slug != "shared-b2" {
		t.Fatalf("by folder: %q %q, %v", name, slug, err)
	}
	if f := c.Circles[1]; !f.Resync || f.resyncMode() != resyncKeepNewer || c.Circles[0].Resync {
		t.Errorf("the request went to the wrong folder or mode: %+v", c.Circles[:2])
	}
	if _, _, err := c.requestFullSync("shared-a1", "this"); err != nil || c.Circles[0].resyncMode() != resyncThisDevice {
		t.Errorf("by slug, keeping this device's copies: %v, %q", err, c.Circles[0].resyncMode())
	}
	if _, _, err := c.requestFullSync("Shared", "hub"); err != nil || c.Circles[0].resyncMode() != resyncKeepHub {
		t.Errorf("keeping the hub's copies: %v, %q", err, c.Circles[0].resyncMode())
	}
	if _, _, err := c.requestFullSync("Shared", "mine"); err == nil {
		t.Error("an unknown choice was accepted")
	}
	if _, _, err := c.requestFullSync("Shared", ""); err == nil {
		t.Error("no choice was accepted: the person has to say whose copies win")
	}
	if _, _, err := c.requestFullSync("Old", "hub"); err == nil {
		t.Error("a folder removed from this device was accepted")
	}
	if _, _, err := c.requestFullSync("Nope", "hub"); err == nil {
		t.Error("a folder that is not here was accepted")
	}
	// a folder that only receives or only sends here never bisyncs, and one waiting for its key cannot sync at all
	c.Circles = append(c.Circles,
		CircleState{CircleConfig: model.CircleConfig{Slug: "ro", DisplayName: "Photos", Role: "readonly"}, Folder: "Photos"},
		CircleState{CircleConfig: model.CircleConfig{Slug: "in", DisplayName: "Inbox", SyncMode: model.ModeReceiveOnly}, Folder: "Inbox"},
		CircleState{CircleConfig: model.CircleConfig{Slug: "key", DisplayName: "Keyless"}, Folder: "Keyless", NeedsKey: true},
	)
	for _, folder := range []string{"Photos", "Inbox", "Keyless"} {
		if _, _, err := c.requestFullSync(folder, "hub"); err == nil {
			t.Errorf("%s: a full sync was accepted for a folder that cannot take one", folder)
		}
	}
	// a full sync the agent schedules itself (an arrival, a rename) keeps this device's copies, as it always has
	auto := CircleState{Resync: true}
	if auto.resyncMode() != resyncThisDevice || (CircleState{}).resyncMode() != noResync {
		t.Errorf("modes: scheduled %q, none %q", auto.resyncMode(), CircleState{}.resyncMode())
	}
	for mode, want := range map[string][]string{
		noResync:         nil,
		resyncThisDevice: {"--resync"},
		resyncKeepHub:    {"--resync-mode", "path2"},
		resyncKeepNewer:  {"--resync-mode", "newer"},
	} {
		args := bisyncArgs(CircleState{}, "/l", "R:", "/w", mode)
		got := []string{}
		for i, a := range args {
			if a == "--resync" {
				got = append(got, a)
			}
			if a == "--resync-mode" && i+1 < len(args) {
				got = append(got, a, args[i+1])
			}
		}
		if !slices.Equal(got, want) && !(len(want) == 0 && len(got) == 0) {
			t.Errorf("mode %q: %v, want %v", mode, got, want)
		}
	}
}

// The command a person runs reaches the running agent through its loopback page and asks for the full sync there
// (docs/adr/0023), so the agent, which holds the config, is the one that changes it and starts the sync at once.
func TestAFullSyncAskedForFromTheCommandLineReachesTheRunningAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	_ = os.MkdirAll(AppDir(), 0o700)
	token := "tok-" + strconv.Itoa(os.Getpid())
	cfg := Config{Version: 1, DeviceID: 4, UIToken: token, Circles: []CircleState{
		{CircleConfig: model.CircleConfig{Slug: "trip-x2a4", DisplayName: "Trip"}, Folder: "Trip", KeySealed: "k", KeyGen: 1},
	}}
	a := &Agent{store: &Store{path: ConfigPath(), cfg: cfg}, logger: log.New(io.Discard, "", 0), syncNow: make(chan string, 1), st: LoadState()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := a.serveLocalUI(ctx, 0, token)
	if port == 0 {
		t.Fatal("the loopback page did not start")
	}
	_ = a.store.Update(func(c *Config) { c.UIPort = port })

	if _, err := AskForFullSync("Trip", "sideways"); err == nil {
		t.Error("an unknown choice was accepted")
	}
	if a.store.Config().Circles[0].Resync {
		t.Fatal("a refused request changed the folder")
	}
	name, err := AskForFullSync("trip", "hub")
	if err != nil || name != "Trip" {
		t.Fatalf("request: %q, %v", name, err)
	}
	if f := a.store.Config().Circles[0]; !f.Resync || f.resyncMode() != resyncKeepHub {
		t.Errorf("the agent's config: %+v", f)
	}
	select {
	case slug := <-a.syncNow:
		if slug != "trip-x2a4" {
			t.Errorf("started a sync of %q", slug)
		}
	default:
		t.Error("the full sync was recorded but not started")
	}
	if b, _ := os.ReadFile(ConfigPath()); !strings.Contains(string(b), `"resync_keep": "hub"`) {
		t.Error("the request did not reach the config on disk, so a restart would forget it")
	}
	if _, err := AskForFullSync("Nope", "hub"); err == nil || !strings.Contains(err.Error(), "no folder called") {
		t.Errorf("a folder that is not here: %v", err)
	}
}

// A person's request made while a scheduled full sync is already running outlives that sync (docs/adr/0023): the sync
// that finishes clears only what it was started with.
func TestARequestMadeDuringAFullSyncOutlivesIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := Config{Circles: []CircleState{{CircleConfig: model.CircleConfig{Slug: "trip", DisplayName: "Trip"}, Folder: "Trip", Resync: true, Refused: true}}}
	a := &Agent{store: &Store{path: filepath.Join(t.TempDir(), "config.json"), cfg: cfg}, logger: log.New(io.Discard, "", 0)}
	started := a.store.Config().Circles[0]
	_ = a.store.Update(func(c *Config) { _, _, _ = c.requestFullSync("Trip", "hub") })
	a.syncSucceeded(started)
	if f := a.store.Config().Circles[0]; !f.Resync || f.ResyncKeep != "hub" || f.Refused {
		t.Errorf("after the running sync finished: %+v; want the hub request still pending and the refusal over", f)
	}
	a.syncSucceeded(a.store.Config().Circles[0])
	if f := a.store.Config().Circles[0]; f.Resync || f.ResyncKeep != "" {
		t.Errorf("after the requested sync finished: %+v", f)
	}
}

// The status a person reads on a support call says when a folder is refused and gives the command that settles it,
// with the helper's full path, since it is not on the PATH (docs/adr/0023). A folder failing for another reason is
// not told to take a full sync.
func TestStatusGivesARefusedFolderTheCommandThatSettlesIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", filepath.Join(os.Getenv("HOME"), "AppData", "Local"))
	helper := filepath.Join(AppDir(), "qp")
	if runtime.GOOS == "windows" {
		helper += ".cmd"
	}
	refused := CircleState{CircleConfig: model.CircleConfig{Slug: "trip", DisplayName: "Trip"}, Folder: "Trip", Refused: true}
	line := folderState(refused, model.CircleHealth{LastError: "Failed to bisync: all files were changed", Failures: 4})
	for _, want := range []string{"refused 4 times", "all files were changed", strconv.Quote(helper) + ` resync "Trip" --keep this|hub|newer`} {
		if !strings.Contains(line, want) {
			t.Errorf("refused folder: %q lacks %q", line, want)
		}
	}
	offline := folderState(CircleState{Folder: "Trip"}, model.CircleHealth{LastError: "connect: no route to host", Failures: 4})
	if strings.Contains(offline, "resync") || !strings.Contains(offline, "no route to host") {
		t.Errorf("failing for another reason: %q", offline)
	}
}

// An update replaces the bundle's programs all together or not at all (2026-09-14). On Owl the swap stopped at the
// daemon, after the agent and rclone had already been replaced, and every later attempt then failed on the agent:
// the files on disk were a mix of two versions. A swap that cannot finish puts back every file it replaced.
func TestAnUpdateSwapIsAllOrNothing(t *testing.T) {
	files := []string{"qpsync-agent.exe", "rclone.exe", "Quietport Network.exe", "tailscale.exe"}
	setup := func() (app, stage string) {
		app, stage = t.TempDir(), t.TempDir()
		for _, f := range files {
			_ = os.WriteFile(filepath.Join(app, f), []byte("old "+f), 0o755)
			_ = os.WriteFile(filepath.Join(stage, f), []byte("new "+f), 0o755)
		}
		return app, stage
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	app, stage := setup()
	if err := swapBundle(app, stage, files, osSwapFS); err != nil {
		t.Fatalf("a swap with nothing in its way: %v", err)
	}
	for _, f := range files {
		if read(filepath.Join(app, f)) != "new "+f || read(filepath.Join(app, f+".prev")) != "old "+f {
			t.Errorf("%s after the swap: %q, previous %q", f, read(filepath.Join(app, f)), read(filepath.Join(app, f+".prev")))
		}
	}

	app, stage = setup()
	failing := osSwapFS
	failing.rename = func(from, to string) error {
		if filepath.Base(from) == "Quietport Network.exe" && strings.HasSuffix(to, ".prev") {
			return errors.New("Access is denied.")
		}
		return os.Rename(from, to)
	}
	if err := swapBundle(app, stage, files, failing); err == nil {
		t.Fatal("a swap that could not move the daemon reported success")
	}
	for _, f := range files {
		if got := read(filepath.Join(app, f)); got != "old "+f {
			t.Errorf("%s after a failed swap is %q, want the old program back", f, got)
		}
	}
}

// A backup a swap cannot delete is moved out of its way (2026-09-14). Windows will not delete the file of a running
// program but will rename it, and the daemon an earlier update left behind runs from "<daemon>.prev", so deleting
// that backup failed and the swap stopped with "Access is denied" on every attempt.
func TestASwapMovesABackupItCannotDeleteOutOfTheWay(t *testing.T) {
	app, stage := t.TempDir(), t.TempDir()
	d := "Quietport Network.exe"
	_ = os.WriteFile(filepath.Join(app, d), []byte("current"), 0o755)
	_ = os.WriteFile(filepath.Join(app, d+".prev"), []byte("still running"), 0o755)
	_ = os.WriteFile(filepath.Join(stage, d), []byte("new"), 0o755)
	held := osSwapFS
	held.remove = func(p string) error {
		if filepath.Base(p) == d+".prev" {
			return errors.New("Access is denied.")
		}
		return os.Remove(p)
	}
	if err := swapBundle(app, stage, []string{d}, held); err != nil {
		t.Fatalf("swap with a backup still running: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(app, d)); string(b) != "new" {
		t.Errorf("the new daemon is not in place: %q", b)
	}
	aside, _ := filepath.Glob(filepath.Join(app, d+".prev-*"))
	if len(aside) != 1 {
		t.Fatalf("the running backup was not moved aside: %v", aside)
	}
	if b, _ := os.ReadFile(aside[0]); string(b) != "still running" {
		t.Errorf("moved aside: %q", b)
	}
	// a later start clears what it can of those
	removeAsideBackups(app)
	if left, _ := filepath.Glob(filepath.Join(app, "*.prev-*")); len(left) != 0 {
		t.Errorf("backups moved aside were not cleared: %v", left)
	}
}

// An update that could not be installed is not downloaded again at every heartbeat (2026-09-14). Owl fetched the
// 60 MB bundle every 5 minutes for 11 hours and failed the same way each time. A failure waits an hour and is
// reported to the hub, so the operator can see why a device is not updating.
func TestAFailedUpdateWaitsAnHourBeforeTheNextDownload(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	failed := updateState{Version: "0.1.27", At: now, Failed: "update swap qpsync-agent.exe: Access is denied."}
	for _, c := range []struct {
		why   string
		s     updateState
		ok    bool
		after time.Duration
		want  bool
	}{
		{"nothing recorded", updateState{}, false, 0, true},
		{"failed 5 minutes ago", failed, true, 5 * time.Minute, false},
		{"failed 61 minutes ago", failed, true, 61 * time.Minute, true},
		{"a different version failed", updateState{Version: "0.1.26", At: now, Failed: "x"}, true, 5 * time.Minute, true},
	} {
		if got := updateDue(c.s, c.ok, "0.1.27", now.Add(c.after)); got != c.want {
			t.Errorf("%s: due %v, want %v", c.why, got, c.want)
		}
	}
}

// An agent asks the hub every 30 seconds whether a key waits for it (docs/adr/0028). The answer is read from the
// hub's reply, and a hub that does not know the question (older than 0.1.29, 404) means no heartbeat is due.
func TestAnAgentReadsWhetherAKeyWaitsForIt(t *testing.T) {
	waiting := true
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/due" || r.Header.Get("Authorization") != "Bearer dev-token" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"heartbeat":%t}`, waiting)
	}))
	defer hub.Close()
	c := &HubClient{base: hub.URL, token: "dev-token", http: hub.Client()}
	if due, err := c.Due(context.Background()); err != nil || !due {
		t.Fatalf("a key waits: Due = %v, %v", due, err)
	}
	waiting = false
	if due, err := c.Due(context.Background()); err != nil || due {
		t.Fatalf("nothing waits: Due = %v, %v", due, err)
	}
	old := &HubClient{base: hub.URL + "/older-hub", token: "dev-token", http: hub.Client()}
	if due, _ := old.Due(context.Background()); due {
		t.Fatal("a hub without the question made a heartbeat due")
	}
}
