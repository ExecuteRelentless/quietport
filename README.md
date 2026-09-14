# Quietport

A private, invite-only shared folder. Files land in a normal folder on each member's Mac, PC or Linux machine,
encrypted before they leave the computer, and only the people in that folder hold the key. There are no accounts to
log into, no interface to learn, and the server never has a key.

This repository holds the whole system: the hub that runs on one small Linux VM, the operator CLI, the member client,
the installers and the public site. MIT licensed. Current release: 0.1.15.

## How it works

- A **folder** ("circle") is a bucket of encrypted objects on the hub plus a key that exists only on member devices.
  The client is rclone's `crypt` backend (XSalsa20-Poly1305 for content, encrypted file names) running a two-way sync
  every minute, with a 30-day version history per folder.
- Devices reach the hub over a private WireGuard mesh (Headscale, userspace Tailscale inside the client). Storage
  is Garage, an S3-compatible object store bound to the mesh address only, one bucket per folder with a size quota.
- **Invites are links.** A member opens **Share a folder** (a shortcut inside every synced folder), types a name, and
  gets a one-time link that expires in 24 hours. The folder key is sealed under the link's code on the member's own
  computer; the hub stores only a hash of the code and the sealed blob. The person who opens the link downloads
  `Quietport.dmg` or `Quietport.exe` (inside a zip), clicks Continue, and the folder appears. No email, no password.
  Someone who already has Quietport pastes the link on **Share a folder** instead and joins without downloading
  anything; the folders already on their computer stay as they are.
- **Anyone can start a folder** from the same installer ("Start a new folder") when the hub's `open_signup` setting is
  on, and any member can start additional folders from the Share page. The founder owns the folder: the owner sees
  the people in it and can remove one, which re-keys the folder from the owner's device so the removed computer gets
  nothing new.
- The client updates itself: signed releases published on the hub, verified against an ed25519 key compiled into the
  client, with rollback if the new version fails to start.
- Everything the operator does goes through `qpctl` and lands in an append-only audit log.

## What is in the repository

```
cmd/qp-hub            hub: public site, invite pages, downloads, agent + operator API, TLS front for Headscale
cmd/qpctl             operator CLI (the only admin interface)
cmd/qpsync-agent      member client: userspace tailscaled + rclone bisync per folder, heartbeat, Share page, self-update
cmd/qp-installer      the installers: notarized Mac app inside a DMG, single Windows exe, Linux binary (client embedded)
internal/             model, hubdb (SQLite), cryptobox, cred (Keychain / DPAPI), agent, shamir (vendored, MPL-2.0)
deploy/               hub bootstrap, install and configure scripts, systemd units, Headscale and Garage configs, backups
installers/           icons, and qp-sidebar (Finder sidebar helper, Objective-C, universal binary + source)
scripts/build.sh      cross-builds hub, client bundles and operator kit, signs the release
scripts/build-installer.sh   builds, signs and notarizes the Mac app and DMG, embeds the client in the Windows and Linux installers
.github/workflows     builds the Windows binaries from source on every tag; signs them through SignPath once configured
docs/RUNBOOK.md       how to operate it
docs/SIGNING.md       Windows code signing (SignPath Foundation, free for open source)
```

## Running your own hub

About 15 minutes on a small VM.

1. A Linux VM (Ubuntu 24.04, 2 vCPU, 4 GB, arm64 or x86-64) with 22/tcp, 443/tcp and 41641/udp open, and a DNS name
   pointing at it. No domain yet? `<ip-with-dashes>.sslip.io` resolves to the IP for free and gets a real
   Let's Encrypt certificate.
2. Unpack `quietport-hub-linux-<arch>.tar.gz` on the VM, then as root:
   ```
   bash deploy/hub-bootstrap.sh                  # hardening, Headscale, Garage, rclone
   bash deploy/hub-install.sh                    # qp-hub, qpctl, backup jobs, systemd unit
   bash deploy/hub-configure.sh <your hostname>  # configs, mesh, storage, TLS
   sudo -u quietport env $(cat /etc/quietport/hub.env | xargs) qp-hub init-operator   # prints your operator token
   ```
   `QP_OPERATOR_NAME` in `/etc/quietport/hub.env` is what members see on operator-made invites; leave it as
   `Quietport` unless you want a person's name there.
3. Build and publish the installers (step 5 below), then either turn on open signup so anyone with the download can
   start a folder:
   ```
   ssh -N -L 18443:127.0.0.1:8443 <vm> &
   ./qpctl init --hub http://127.0.0.1:18443 --token <operator token> --operator <you>
   ./qpctl settings open_signup=1 signup_quota=5368709120
   ```
   or create the first folder yourself and invite yourself into it:
   ```
   ./qpctl circle create circle-1 --name "Circle 1" --quota 20G
   ./qpctl person add you && ./qpctl invite create you --circles circle-1
   ```
   Open that link on your own computer. Once your computer is a member, `qpctl` can reach the hub through the client's
   local proxy instead of the ssh tunnel: `qpctl init --hub http://<hub tailnet ip>:8443 --proxy
   socks5://127.0.0.1:<port>` (the port is in the client's log line `--socks5-server=`).
4. From then on members invite each other from **Share a folder**; you only watch `qpctl status`.
5. Building the installers needs a Mac with Xcode command line tools, Go 1.27, an Apple Developer ID certificate for
   signing and an App Store Connect API key for notarization:
   ```
   scripts/build.sh 0.1.14
   NOTARY_KEY=<AuthKey.p8> NOTARY_KEY_ID=<id> NOTARY_ISSUER=<issuer> scripts/build-installer.sh 0.1.14 <your hostname>
   scp dist/0.1.14/* <vm>:/tmp/ && ssh <vm> sudo bash deploy/publish-release.sh 0.1.14
   ```
   Clients on older versions update themselves within a few minutes of a publish.

## Building

- Go 1.27. `scripts/build.sh` expects the third-party client binaries in `../vendor-bins` (outside the repo):
  `rclone/rclone-v1.75.1-<os>-<arch>/rclone` unpacked from the rclone releases, and `ts-<os>-<arch>/tailscaled` +
  `tailscale` built from source with `go build tailscale.com/cmd/tailscaled@v1.102.3` (and `cmd/tailscale`) for each
  platform, exactly as `.github/workflows/windows.yml` does for Windows.
- The release signing key pair lives in `../release-keys/` (`release.key` never leaves the operator's machine;
  `release.pub` is compiled into the client). `scripts/sign` creates one.
- `installers/mac/qp-sidebar` is prebuilt from `qp-sidebar.m`:
  `clang -fobjc-arc -framework Foundation -framework CoreServices -arch arm64 -arch x86_64 -o qp-sidebar qp-sidebar.m`.
- Windows binaries are unsigned until the SignPath signing job is configured (`docs/SIGNING.md`); SmartScreen shows
  "More info / Run anyway" once and Defender may need a false-positive report in the meantime.

## Working on the code

- `CONTEXT.md` is the shared language (circle, person, device, owner, operator, invite, grant, bundle). Use its
  terms in code, docs and commits; add to it when a new concept appears.
- `docs/adr/` records the decisions that are not obvious from the code, one file each. A decision is superseded by
  a new record, never edited away.
- Tests: `go test ./...`. They cover the seams that matter (sealing and signing, invite consume-once, append-only
  audit, migrations, the folder-creation gates, self-update version logic, the marker). Add a failing test with every
  bug fix. CI (`.github/workflows/ci.yml`) runs gofmt, vet and the tests on every push and pull request.
- `git config core.hooksPath scripts/githooks` installs a pre-commit hook that refuses unformatted Go, vet errors and
  failing tests.
- `scripts/wizard-domain-cutover.sh` walks the operator through moving the hub to a real domain without breaking
  enrolled devices.

## Security model in one paragraph

The hub stores ciphertext with encrypted names, holds no folder key and no member password, and cannot read anything
even if the VM is copied. Keys travel only inside sealed invite blobs (derived from the one-time link code) and
per-device sealed grants (x25519), and each folder key can be rotated by the owner. Every device is added on purpose
through a link; nothing is public. What the hub can see: which devices exist, when they last checked in, how many
bytes each folder holds, and the folder names members typed. What is not protected: a member who already has a file
keeps it after being removed, like any file ever sent to someone, and a lost laptop with an unlocked session has the
key until it is removed and the folder re-keyed. The client trusts its own signed binary; there is no web viewer, so
the server never serves code that touches a key.

## Licence

MIT for Quietport's own code. Bundled or depended on: rclone (MIT), Tailscale client (BSD-3), Headscale (BSD-3),
Garage (AGPL-3, server side only), Vault's shamir package (MPL-2.0, `internal/shamir/LICENSE`).
