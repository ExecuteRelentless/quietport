# 0018 Every program the agent starts is built by one constructor

**Context.** 0.1.23 made the Windows agent a GUI-subsystem program so that the Scheduled Task could not open a
console window for it (docs/adr/0017). That fixed the window the member saw at logon and immediately produced a
different one: a black window flashing on screen roughly once a minute, in pairs.

A process with no console of its own does not lend one to its children. Windows allocates a brand new console, with
a visible window, for every console program such a process starts, unless the parent passes `CREATE_NO_WINDOW`.
Almost every call site in `internal/agent` already did, through `hideWindow`. Two did not: the `rclone obscure`
calls inside `Rclone.env`, one for the password and one for the salt. `env` builds the environment for every rclone
run, and the sync loop runs every 60 seconds by default, so those two spawns became two window flashes a minute for
as long as a member owned the product.

They were invisible before 0.1.23 for the reason the release exists: the agent had a console of its own, so its
children attached to that console and drew nothing new. **The defect was masked by the defect being fixed**, which
is why no amount of care over the 0.1.23 diff would have found it by reading. It took a member watching a screen.

**Decision.** `command` and `commandContext` in `internal/agent/proc.go` build every program the agent starts, and
they apply `hideWindow` themselves. No file in the package calls `exec.Command` or `exec.CommandContext` directly,
and `TestEveryProcessTheAgentStartsGoesThroughOneConstructor` reads the package's own source to keep it that way;
it failed on 41 call sites before the change. The explicit `hideWindow` calls scattered through `install.go`,
`rclone.go`, `tailscale.go`, `rotate.go`, `update.go` and `notify.go` are gone, because a guard that has to be
remembered at 41 sites is not a guard. On macOS and Linux `hideWindow` does nothing and these are plain
`exec.Command`.

**Consequence.** Adding a call to a program is no longer a decision about console windows; the door that exists is
the safe one and the test refuses a second door. The test reads source text rather than behaviour, which is weak
evidence about a running program and strong evidence about this particular class, since the failure is always a
call site that forgot. The window itself still has no seam on a build runner, for the reason ADR 0017 gives, so a
release is still checked by hand in a real Windows session before it is published.
