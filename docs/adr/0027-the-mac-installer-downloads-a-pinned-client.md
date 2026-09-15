# 0027 The Mac installer downloads the client for its own chip, and installs only the client bundle it was built with

**Context.** The Mac disk image was 128 MB because `build-installer.sh` merged every client program for both chips
into the installer app; each member's computer used half of it. A browser cannot tell the installer which chip it
runs on (Safari on Apple Silicon reports an Intel Mac), so two images would ask members a question they cannot
answer. The installer already had a fallback that downloads a client bundle when the app carries none, but that
fallback checked nothing, and "Start a new folder" (`runStart`) ignored a missing client entirely and would have
installed without programs. The host it downloads from is the one the image came from, or the one in a pasted link,
so an unchecked download would let any site serving our generic image make a notarized Quietport installer run its
code; today such a site can only hand it a mesh configuration.

**Decision.** The Mac installer app carries no client (`bundled_darwin.go` is gone; the Mac build uses the stub that
reports none). On the computer it runs on it downloads `/dl/quietport-darwin-<arch>-<its own version>.tar.gz` from the
hub, the client bundle self-update installs, and extracts it only when its sha256 equals the one compiled into the
notarized installer for that chip (`-X main.clientSumArm64/Amd64`, computed by `build-installer.sh` from
`dist/<version>`). An installer built without the sums installs nothing it downloads. The invite path and "Start a
new folder" both go through `installClient`. The download resumes and watches for stalls exactly like self-update
(`agent.NewDownloader`, the same `Download`), with 5 tries 2 to 8 seconds apart. Before downloading, an invite
install asks the invite page whether the link still works (it answers 404 for a dead link without using it up), so
nobody downloads 50 MB to learn the link is gone. Windows installers still carry the client; a Linux installer
without its client stops with a sentence instead of fetching the Mac bundle, as the old fallback's URL would have.
`TestTheMacInstallerInstallsOnlyTheBundleItWasBuiltFor` failed first, and fails again with the hash check removed;
`TestADeadLinkIsKnownBeforeTheClientIsDownloaded` fails with the check answering "live" every time. Checked live
against the hub on 2026-09-15 with a build pinned to 0.1.28: all 7 files downloaded, verified and extracted in 6.7 s,
and a wrong pin was refused.

**Consequences.** A first install now runs the ad-hoc signed programs a self-update installs, instead of copies
signed with the Developer ID; the vault key is reached through `/usr/bin/security`, so the programs' signature does
not matter there, and every Mac that has self-updated already runs them. An installer fetches its own version, so the
hub must keep every released Mac client bundle (it keeps them all today), and they must be published no later than
the installer. A rebuilt tarball invalidates the pins and every new Mac install would fail, so `build.sh` and
`build-installer.sh` run once, in that order, per version, and after publishing the sha256 of each live tarball is
compared with the pins `build-installer.sh` printed (RUNBOOK).
