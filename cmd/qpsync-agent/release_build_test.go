package main

import (
	"os"
	"strings"
	"testing"
)

// The console window is gone only if the binaries that ship are GUI-subsystem programs (docs/adr/0017). The CI job
// that reads the subsystem byte builds its own copy of the agent, so it would stay green if the flag were dropped
// from the two builds that actually produce a release. This pins both of them.
func TestBothReleaseBuildsMakeTheAgentAGUIProgram(t *testing.T) {
	for _, c := range []struct{ path, flag string }{
		{"../../scripts/build.sh", `agentflags="$flags -H windowsgui"`},
		{"../../.github/workflows/windows.yml", `-ldflags "$LD -H windowsgui" -o out/client/qpsync-agent.exe`},
	} {
		b, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		if !strings.Contains(string(b), c.flag) {
			t.Errorf("%s no longer builds the agent with -H windowsgui (looked for %q): Windows would give it a console window again", c.path, c.flag)
		}
	}
}
