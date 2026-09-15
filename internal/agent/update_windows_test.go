package agent

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Stands in for a running daemon when started by the test below; skipped otherwise.
func TestHelperStandInDaemon(t *testing.T) {
	if os.Getenv("QP_STAND_IN_DAEMON") == "" {
		t.Skip("only runs as a stand-in daemon")
	}
	time.Sleep(3 * time.Minute)
}

// What happened on Owl on 2026-09-13, on a real Windows: a self-update renames the running daemon's file to ".prev".
// The next agent must recognise that daemon by the name Windows now reports and stop it, and the next update must be
// able to swap while that backup is still running.
func TestAnOrphanedDaemonIsStoppedAndTheNextUpdateSwaps(t *testing.T) {
	app, stage := t.TempDir(), t.TempDir()
	d := daemonName("windows")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copyFile := func(from, to string) {
		in, err := os.Open(from)
		if err != nil {
			t.Fatal(err)
		}
		defer in.Close()
		out, err := os.Create(to)
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		if _, err := io.Copy(out, in); err != nil {
			t.Fatal(err)
		}
	}
	copyFile(self, filepath.Join(app, d))
	cmd := exec.Command(filepath.Join(app, d), "-test.run=^TestHelperStandInDaemon$")
	cmd.Env = append(os.Environ(), "QP_STAND_IN_DAEMON=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// the swap 0.1.25's updater did: the running daemon's file becomes the backup, the new one takes its name
	if err := os.Rename(filepath.Join(app, d), filepath.Join(app, d+".prev")); err != nil {
		t.Fatalf("Windows refused to rename a running program's file: %v", err)
	}
	_ = os.WriteFile(filepath.Join(app, d), []byte("the daemon 0.1.25 placed"), 0o755)
	var reported string
	for i := 0; i < 50 && reported == ""; i++ {
		for _, p := range runningProcs() {
			if p.PID == cmd.Process.Pid {
				reported = p.Exe
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("Windows reports the running daemon as %s", reported)
	// the diagnosis rests on this: were the old name reported, the cleanup would already have stopped it
	if !strings.HasSuffix(strings.ToLower(reported), ".prev") {
		t.Errorf("Windows reports the renamed daemon as %q, not by its new name", reported)
	}
	if !slices.Contains(leftoverDaemons(runningProcs(), app), cmd.Process.Pid) {
		t.Errorf("the daemon left running from %s is not recognised as a leftover", reported)
	}

	// the next update, with that backup still running
	_ = os.WriteFile(filepath.Join(stage, d), []byte("the next daemon"), 0o755)
	if err := swapBundle(app, stage, []string{d}, osSwapFS); err != nil {
		t.Errorf("the next update could not swap while the old daemon ran: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(app, d)); !strings.Contains(string(b), "the next daemon") {
		t.Errorf("the next daemon is not in place: %q", b)
	}

	if n := stopLeftoverDaemons(app); n < 1 {
		t.Errorf("stopped %d leftover daemons, want the stand-in", n)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Error("the leftover daemon is still running")
	}
}
