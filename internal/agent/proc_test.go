package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Since 0.1.23 the agent is a GUI-subsystem program with no console of its own (docs/adr/0017). Windows gives any
// console child of a process with no console a brand new console window, so every program the agent starts has to
// be created with CREATE_NO_WINDOW or it flashes a black window on the member's screen. Two rclone calls inside
// env() were missed, and because env() runs on every sync the flash came back once a minute, in pairs, on 0.1.23.
// One constructor is the only way to keep that from happening again, so nothing in this package may call
// exec.Command directly.
func TestEveryProcessTheAgentStartsGoesThroughOneConstructor(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	raw := regexp.MustCompile(`exec\.Command(Context)?\(`)
	for _, f := range files {
		if f == "proc.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if raw.MatchString(line) {
				t.Errorf("%s:%d starts a program without the console-window guard: use command/commandContext\n    %s",
					f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
