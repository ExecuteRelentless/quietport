# 0011 On Windows the agent runs tailscaled in Unattended Mode

**Context.** Tailscale on Windows assumes a tray app that stays connected to tailscaled's pipe while its user is
signed in. When the last client disconnects, the backend drops its current user and asks for a background profile.
With Always-On off there is none (Always-On is a policy setting, and the Quietport daemon has no policy store,
ADR 0010), so it disconnects and stays idle until a client connects again. At startup it resumes a profile only
from the server-mode key, which only Unattended Mode writes. The Quietport agent is not a tray app: it connects for
the length of one `tailscale status` call. So the mesh, and the SOCKS proxy that rclone and the hub client go
through, would be up only during those calls, and nothing would log back in after a restart. macOS and Linux keep
the profile with no client connected. (tailscale 1.102.3: `ipn/ipnserver/server.go` addActiveHTTPRequest,
`ipn/ipnlocal/local.go` background profile, `ipn/desktop/extension.go`, `ipn/ipnlocal/profiles.go` readAutoStartKey.)

**Decision.** `tailscale up` on Windows carries `--unattended` (`upArgs` in `internal/agent/tailscale.go`;
`TestUpArgsUnattendedOnWindows` failed before the change). Other platforms have no such flag and are unchanged.

**Consequences.** Verified end to end on hosted runners (branch `diag/windows-e2e`, run 34628325030), installing as
a standard local user against the live hub on Windows Server 2022 x64 and on Windows 11 ARM64 build 26200, where the
x64 client runs under emulation as it does in a Windows VM on an Apple Silicon Mac. With the 0.1.16 agent the device
registered, then tailscaled logged "client disconnected: disconnecting Tailscale" after every CLI call and the
install failed on both ("the hub did not" on x64, "could not join the network" on ARM64). With the flag it logged
"staying on profile", the install finished in 7 and 10 s, the daemon the background agent restarts came up on its own
(`serverMode=true`), and 40 s later the hub answered through the SOCKS proxy and the folder had synced. Unattended
Mode is a pref kept in the daemon's state, so it applies from the next `up`; no Windows device had enrolled before
this change, so none carries the old pref.
