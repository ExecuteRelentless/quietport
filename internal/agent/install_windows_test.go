//go:build windows

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The repeating trigger is worth something only if Windows accepts it. schtasks rejects a task whose XML is out of
// order as a whole, which would leave a device with no task at all, so this registers the real XML on this computer
// and reads back what the Task Scheduler stored. It is one of the two halves of docs/adr/0017 a runner can prove;
// the missing console window needs an interactive logon and is checked by hand before a release.
func TestWindowsAcceptsTheRepeatingTask(t *testing.T) {
	if _, err := exec.LookPath("schtasks"); err != nil {
		t.Skip("no schtasks on this computer")
	}
	const name = "QuietportTaskXMLTest"
	f := filepath.Join(t.TempDir(), "task.xml")
	if err := os.WriteFile(f, utf16le(taskXML(taskUser(), `C:\Windows\System32\cmd.exe`)), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("schtasks", "/Create", "/TN", name, "/XML", f, "/F").CombinedOutput(); err != nil {
		t.Fatalf("Windows refused the task: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("schtasks", "/Delete", "/TN", name, "/F").Run() })
	out, err := exec.Command("schtasks", "/Query", "/TN", name, "/XML").CombinedOutput()
	if err != nil {
		t.Fatalf("reading the task back: %v: %s", err, out)
	}
	if taskNeedsRefresh(string(out)) {
		t.Fatalf("the Task Scheduler kept no repetition:\n%s", out)
	}
}
