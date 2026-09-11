# 0010 tailscaled runs unelevated on Windows, so the Windows daemon is built with two flags

**Context.** Quietport installs without administrator rights: tailscaled runs as the member's own process, not as
a Windows service. Stock tailscaled expects to be a service running as LocalSystem, and two things in it assume that.
Its control pipe is created with a security descriptor that names BUILTIN\Administrators as the owner, and Windows
refuses that owner from any non-elevated token (ERROR_INVALID_OWNER, "This security ID may not be assigned as the
owner of this object"), so the daemon exited at once and every real Windows install failed with "this computer could
not reach the network" (2026-09-10). And when it is not LocalSystem it registers a policy store for the current user,
whose Group Policy lock a standard user's session cannot take ("Access is denied"), which kills the backend right after
the pipe opens. Nothing of this reached a log: the install step threw the daemon's output away and slept a fixed
1.5 s before `tailscale up`.

**Decision.** `scripts/build-tailscale.sh` builds the Windows daemon from unpatched upstream source with
`-tags ts_omit_syspolicy` (no Windows policy store; a Quietport device is never managed by Group Policy) and
`-X tailscale.com/safesocket.windowsSDDL=D:PAI(A;OICI;GWGR;;;BU)(A;OICI;GWGR;;;SY)` (the stock access list with no
owner, so the pipe belongs to whoever creates it). Other platforms stay stock. The Mac release build and CI both use
the script. The install step now waits for the daemon to answer on its socket, up to 30 s, and writes the daemon's
output to the agent log, so a daemon that dies is reported as "the network service could not start" with the reason
on disk.

**Consequences.** Verified on a hosted Windows runner for an elevated administrator, a UAC-filtered administrator and
a standard user with a real logon: stock fails, the pipe change alone fixes administrators, both changes fix all three
(branch `diag/windows-pipe`, `.github/diag/pipeprobe`). Members of the Users group can still read and write the pipe,
and the CLI still connects with the identification impersonation level, unchanged. A tailscale upgrade must re-check
that the variable name and the build tag still exist (`safesocket/pipe_windows.go`, `feature/buildfeatures`), or the
Windows install silently regresses; the diagnostic workflow is the check. MDM and Group Policy settings do not apply
to the Quietport daemon on Windows.
