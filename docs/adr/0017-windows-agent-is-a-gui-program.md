# 0017 On Windows the agent is a GUI program, and its Scheduled Task repeats

**Context.** `qpsync-agent.exe` was a console program, and the per-user Scheduled Task starts it with
`LogonType=InteractiveToken`, so Windows gave it a console window at every logon: black, and empty because the agent
logs to a file. `<Hidden>true</Hidden>` in the task XML hides the task from the Task Scheduler list, not the window.

It was not cosmetic. Closing that window ends the process, and the task's only trigger was a logon trigger, so
nothing started the agent again: `RestartOnFailure` covers a run that fails, not one that was ended. On 2026-09-11 a
fresh install's device sat offline for ten minutes after its window was closed, its "Share a folder" page dead, while
two Macs on the same release kept heartbeating. Every Windows member had the window, 0.1.20 and 0.1.21 alike.

0.1.22 tried to hide the window from inside the process: `GetConsoleWindow` followed by `ShowWindow(SW_HIDE)`, in
`run` only. It did nothing. On Windows 11 the default terminal host is Windows Terminal, so `GetConsoleWindow`
returns the hidden ConPTY dummy window and hiding that changes nothing the member can see. The unit test that
shipped with it proved only which subcommand called the hide, never that the call removed a window, and no runner
can reproduce the window at all: a GitHub runner driven through PsExec has no interactive session, the task reports
`Last Result: 267011` (SCHED_S_TASK_HAS_NOT_RUN) and no agent ever starts. 0.1.22 was built and notarized, failed the
check in a real Windows session, and was never published.

**Decision.** On Windows the agent is built with `-H windowsgui`. A GUI-subsystem program is never allocated a
console, whatever the terminal host is, and that is decided by one field in the PE header before any of the
program's own code runs. It is the one mechanism here that does not depend on runtime behaviour, which is what makes
it a fix rather than another guess; `scripts/build.sh` and the Windows workflow both assert the subsystem byte of the
binary they just built (`scripts/assert-windows-gui.py`, which fails a console build).

Two consequences follow, and both are part of the same change.

The subcommands a person runs in a terminal lose their console too, so everything except `run` calls
`AttachConsole(ATTACH_PARENT_PROCESS)` and rebinds stdout and stderr to `CONOUT$` (`attachesConsole` decides,
`attachConsole` acts). A caller that redirected the output to a pipe or a file already handed over usable handles,
and those are left alone: the check is `GetFileType`, because a GUI program does inherit the parent's console handle
values and they answer `FILE_TYPE_UNKNOWN` when the process has no console. `install-win.ps1` now pipes the agent's
output, because PowerShell does not wait for a GUI-subsystem program unless its output is going somewhere, and
without the pipe the script would return before the install had finished. The single binary stays one binary: a
separate GUI launcher would be a fifth file name, and the updater in every published release copies only the names
it already knows (docs/adr/0015), so a device updating from 0.1.21 would end up with a task pointing at a file that
was never delivered.

Removing the window removes the obvious way to stop the agent but not the others, so the logon trigger now repeats
every five minutes with no duration, indefinitely. `MultipleInstancesPolicy=IgnoreNew` was already set, so the
repetition never starts a second agent beside a healthy one. A device installed on an earlier release keeps its old
task through a self-update, which replaces binaries and nothing else, so the agent re-registers the task when it
starts if what is registered has no `<Repetition>` (`refreshStartup`). It re-registers only a task that is already
there: an install that fell back to the HKCU Run key when `schtasks` failed has no task, and adding one to that
computer would start two agents at every logon.

**Consequence.** The healing starts one logon late. A repetition belongs to the firing of its trigger, and
registering a task does not fire a logon trigger, so on the computer where the agent was just installed or updated
the repetition begins at that person's next sign-in; until then the device behaves as it does today. A time trigger
would start it immediately, at the cost of a failed task run every five minutes on any computer whose owner is not
signed in, because the task runs under an interactive token.

The absent window cannot be regression-tested. No runner has an interactive logon, so there is no
seam for it in CI, and a release is checked by hand in a real Windows session before it is published. What CI does
cover is the rest: the subsystem byte of the built binary, the Scheduled Task XML accepted and read back by a real
Task Scheduler (a trigger written out of order would be rejected whole, leaving a device with no task at all), and a
GUI build's output arriving in the console of the process that ran it, read out of the console screen buffer, with a
second GUI binary carrying none of this code as the control.
