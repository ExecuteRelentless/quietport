# 0015 On Windows the mesh daemon's file carries the product's name

**Context.** The first time the daemon starts on Windows, the Firewall asks the member whether to allow it. The prompt
names the program by the `FileDescription` in its version resource and falls back to the file name; Quietport builds
the daemon from unpatched upstream source (docs/adr/0010), and that build carries no version resource, so the prompt
read "tailscaled.exe". A member has no way to connect that to Quietport: the one security question the product asks
them is about a program whose name appears nowhere in it. The same name is what they see in Task Manager and in the
Firewall's rule list afterwards. Signing the binaries (SignPath) would add a publisher line to the prompt but not
change the program name.

**Decision.** On Windows the daemon file is `Quietport Network.exe` (`daemonName` in `internal/agent/tailscale.go`,
where the process is started and where the updater's file list is built; `TestDaemonNameAndMigration` failed before
the change). macOS and Linux keep `tailscaled`: nothing there shows the member a file name. The build scripts and the
Windows CI workflow write the daemon under that name, so the installer's bundle and every self-update carry it.

A device updating from 0.1.20 or earlier is the one case the bundle cannot fix: that release's updater replaces only
the four file names it knows, so it ignores the renamed daemon in the bundle and then deletes the staging folder.
The 0.1.21 agent therefore renames a leftover `tailscaled.exe` itself on its first start (`migrateDaemon`, called
from `Run`), keeping that device's daemon binary, which is the same upstream build, under the new name. Windows
allows renaming a running executable, so this is safe while the old daemon is still up.

**Consequence.** Nothing may stop the daemon by image name any more: `taskkill /IM tailscaled.exe` would now miss
Quietport's daemon and, worse, still match a real Tailscale install on the same computer. Stop and uninstall match
the running process by its path instead (0.1.21, same release).
