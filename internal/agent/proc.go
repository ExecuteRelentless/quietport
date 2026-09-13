package agent

import (
	"context"
	"os/exec"
)

// command and commandContext build every program the agent starts.
//
// Since 0.1.23 the Windows agent is a GUI-subsystem program, so that the Scheduled Task can no longer open a console
// window for it (docs/adr/0017). A process with no console of its own does not lend one to its children: Windows
// allocates a brand new console, with a visible window, for every console program it starts. The agent starts
// rclone and the mesh CLI constantly, so a single missed CREATE_NO_WINDOW is a black window flashing on a member's
// screen for as long as they own the product. Two of them were missed in 0.1.23 and came back once a minute.
//
// So there is one door, and TestEveryProcessTheAgentStartsGoesThroughOneConstructor keeps it the only one. On macOS
// and Linux hideWindow does nothing and these are plain exec.Command.
func command(name string, arg ...string) *exec.Cmd {
	c := exec.Command(name, arg...)
	hideWindow(c)
	return c
}

func commandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, name, arg...)
	hideWindow(c)
	return c
}
