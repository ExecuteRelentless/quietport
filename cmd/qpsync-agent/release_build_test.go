package main

import (
	"os"
	"strings"
	"testing"
)

// The console window is gone only if the binaries that ship are GUI-subsystem programs (docs/adr/0017). Both
// release paths assert the subsystem byte of the agent they just built, so a dropped flag cannot reach a member;
// this fails first, in the ubuntu job, on any pull request, and covers the one binary neither assertion reads:
// the installer, which is a GUI program for the same reason and has no check of its own.
func TestBothReleaseBuildsMakeTheWindowsBinariesGUIPrograms(t *testing.T) {
	for _, c := range []struct {
		path  string
		wants []string
	}{
		{"../../scripts/build.sh", []string{
			`-H windowsgui`,
			`assert-windows-gui.py`,
		}},
		{"../../.github/workflows/windows.yml", []string{
			`-H windowsgui" -o out/client/qpsync-agent.exe`,
			`-H windowsgui" -o out/Quietport.exe`,
			`assert-windows-gui.py`,
		}},
	} {
		b, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		// whitespace collapsed, so reindenting the build does not fail this
		got := strings.Join(strings.Fields(string(b)), " ")
		for _, want := range c.wants {
			if !strings.Contains(got, want) {
				t.Errorf("%s no longer has %q: a Windows binary would be given a console window again", c.path, want)
			}
		}
	}
}
