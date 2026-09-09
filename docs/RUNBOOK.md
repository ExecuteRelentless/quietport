# Quietport operator runbook

This is the operator's reference for the running system. The specification is `../quietport-srd.pdf`; the build order
and acceptance list in it (sections 10 and 11) are what this runbook is checked against.

## The parts

| Where | What | How it runs |
|---|---|---|
| Hub VM (`ssh quietport-hub`) | `qp-hub` on :443 (site, invites, downloads, TLS front for Headscale) and on `<tailnet ip>:8443` (agent + operator API) | systemd `qp-hub`, env in `/etc/quietport/hub.env` |
| Hub VM | Headscale 0.29 on 127.0.0.1:8080, policy pushed from `/var/lib/quietport/policy` (a git repo) | systemd `headscale` |
| Hub VM | Garage 2.2 object store, S3 API bound to the tailnet address only, one bucket per circle with a size quota | systemd `garage`, config `/etc/garage/garage.toml` |
| Hub VM | nightly encrypted backup to OCI Object Storage bucket `quietport-backup`, weekly canary restore | `/etc/cron.d/quietport`, state in `/var/lib/quietport/backup/*.json` |
| Operator machine | `qpctl` + the keystore (`~/.config/quietport/`, sealed with the login Keychain) | run by hand |
| Each member | `qpsync-agent` + bundled `rclone` and userspace `tailscaled`, all under the user profile | LaunchAgent `app.quietport.agent` (macOS), Scheduled Task `Quietport` (Windows) |

Circle keys exist in exactly 2 places: the operator's keystore and member devices. The hub never has one. Loss of every
copy of a circle's keys makes that circle's files on the hub unrecoverable. There is no escrow, by design (FR-124).

## Day to day

```
qpctl status                      fleet: circles, people, devices, last heartbeats, backup state
qpctl status sarah                one person, with per-circle sync times and conditions
qpctl logs sarah --tail 50        heartbeat history + events for that person's devices
qpctl audit                       append-only audit log of every operator action
```

Conditions reported by agents (visible in `qpctl status`): `quota_exceeded:<slug>`, `path_too_long:<slug>:<n>`,
`corrupt_state:<slug>` (agent has scheduled a full resync), `disk_low`, `clock_skew`, and `relayed` when a device is
going through a DERP relay instead of a direct path (slower; usually a NAT or VPN on the member's side).

## Onboarding

```
qpctl person add sarah --email sarah@example.com --household home
qpctl invite create sarah --circles family,photos          # prints the link; works once; 24 h
```
Send the link however you normally talk to them. They paste one line, see `Quietport is connected.`, and the folders
appear. You see `invite.retrieved` then `device.enrolled` in `qpctl logs sarah`. If the link expires or gets used
before they run it, `qpctl invite revoke <prefix>` and make a new one.


## Members inviting people themselves (no email, no account)

Every QPSync folder contains a shortcut, **Share a folder**. It opens a page on the member's own computer (loopback,
per-install token in the URL) with 1 form: pick a folder, type the person's name, get a link. The link is minted on the
member's device: the circle key is sealed under the code there, the hub stores the hash and the sealed blob, creates a
person record from the typed name (no email), a mesh user and a pre-auth key. Works once, 24 hours.

- Who may do it: read/write members of a circle whose invite policy is `members` (the default). `qpctl circle set
  <slug> --invites operator` restricts a circle to operator-made invites; `--readonly` members never can.
- You see it: `qpctl audit` shows `invite.create` by `device:<id>/<inviter>`, `qpctl person list` shows the new person
  as `<name>-xxxx` with no email, `qpctl logs <inviter>` shows `invite.created`.
- Undo: `qpctl invite revoke <prefix>` before it is used; `qpctl offboard <person> --confirm <person>` after.

## Adding someone to another circle

```
qpctl circle add-member family ben          # key reaches Ben's devices on their next heartbeat (5 min)
qpctl circle add-member newsletter ben --readonly
```

## Offboarding (the whole sequence, FR-115)

```
qpctl offboard ben --confirm ben
```
Revokes Ben's mesh devices, removes his memberships, rotates the key of every circle he was in, re-encrypts those
circles' contents through your machine, pushes the new keys to the remaining members, writes the audit entries, and
reports how long each rotation took. Re-encryption runs through the operator's machine because the hub cannot decrypt;
above an estimated 10 minutes it asks before starting (`--yes` skips the prompt). It says so at the end, and it is
true: nothing he already downloaded can be taken back.

A stolen device, without offboarding the person: `qpctl device revoke <id> --confirm <id>`, then
`qpctl circle rotate-key <slug> --confirm <slug>` for each circle listed in the output.

## Keys: export, split, verify

```
qpctl keys export --out ~/quietport-keys.bundle                 # passphrase-encrypted bundle
qpctl keys split --out ~/qp-shares --shares 5 --threshold 3     # Shamir: bundle + 5 share files
qpctl keys verify                                               # decrypts a real canary object per circle
qpctl keys verify --bundle ~/quietport-keys.bundle              # the same test, using ONLY the bundle
qpctl keys recover --bundle ~/qp-shares/quietport-keys.bundle --share s1.txt --share s2.txt --share s3.txt
```
`qpctl status` nags when `keys verify` has not run in 365 days (FR-123). Run it after every rotation.

## Backups

Nightly at 02:15 the hub snapshots Garage metadata, copies the Headscale DB + noise key, the hub SQLite, the policy
repo and the configs, encrypts everything with rclone crypt (keys in `/etc/quietport/backup.env`, root-only) and syncs
to OCI Object Storage. Sunday 03:45 it restores the canary object and the hub DB from the off-host copy and compares.
`qpctl backup verify` prints both results and fails if either is not `ok`.

A copy of `backup.env` (with the plaintext crypt password and salt) is in `~/Documents/Quietport/secrets/` on the
operator's Mac. Without it the backup is ciphertext.

### Rebuilding the hub from scratch on a fresh VPS
1. New Ubuntu 24.04 VM, ports 22/tcp, 443/tcp, 41641/udp open. `sudo bash deploy/hub-bootstrap.sh`.
2. Restore `/etc/quietport/backup.env`, then `rclone sync crypt:state /tmp/restore` with the same env.
3. Put back `/var/lib/headscale/db.sqlite` + `noise_private.key`, `/etc/headscale/config.yaml`, `/var/lib/quietport/hub.db`,
   `/etc/garage/garage.toml`, `/etc/quietport/hub.env`, then `rclone sync crypt:garage-data /var/lib/garage/data` and the
   latest snapshot from `crypt:garage-meta-snapshots` into `/var/lib/garage/meta` (see Garage docs, `garage meta snapshot`).
4. `sudo bash deploy/hub-install.sh` from a release tarball, then `sudo bash deploy/hub-configure.sh <hostname>`.
5. Point DNS at the new IP. Members reconnect on their own; nothing on their side changes.


## Installers (what members actually download)

- Mac: `Quietport-<code>.dmg`, a notarized disk image holding `Quietport Installer.app`. The app carries the whole client
  (qpsync-agent, rclone, tailscaled, tailscale, qp-sidebar, all universal, all signed) in `Contents/Resources/client`,
  so the only thing it fetches from the hub is the invite payload. It reads the invite code from the disk image's file
  name (via `hdiutil info`) or from the app folder name, and asks for the link if neither carries one.
- Windows: `Quietport-<code>.exe`, the same installer with the client zip embedded (`go:embed`), no console window,
  MessageBox dialogs. Unsigned (no Windows certificate): SmartScreen shows More info / Run anyway once.
- Both still fall back to downloading the client bundle from `/dl/` if a build ships without the embedded files.
- Build: `scripts/build.sh <v>` then `NOTARY_PROFILE=ari-notary scripts/build-installer.sh <v> <hub host>`;
  publish with `deploy/publish-release.sh <v>` on the hub after copying `quietport-installer-darwin.dmg`,
  `quietport-installer-darwin.tar.gz` and `quietport-installer-windows-amd64.exe` to `/tmp` there.

## Releases and self-update

```
scripts/build.sh 0.1.2            # builds hub + client bundles, signs with ../release-keys/release.key
scp dist/0.1.2/quietport-*-0.1.2.* dist/0.1.2/SHA256SUMS.signed quietport-hub:/tmp/
# on the hub, as in deploy/publish-release.sh
```
Agents check for a newer version on every heartbeat, download it over the mesh, verify the sha256 and the ed25519
signature against the key compiled into them, swap binaries, restart, and roll back to the previous version if the
new one fails to start twice (`update.json` in the app dir records attempts). The release private key is
`~/Documents/Quietport/release-keys/release.key` on the operator's Mac. Losing it means agents can never update
again without a reinstall; back it up with the circle keys.

## Support call: what to ask a member to run

macOS: open Terminal and paste `"$HOME/Library/Application Support/Quietport/qp" status`
Windows: open PowerShell and paste `& "$env:LOCALAPPDATA\Quietport\qp.cmd" status`

It prints the hub contact time, connection type, and per-folder last sync + last error, with no file names.

Uninstall: the same path with `uninstall` (add `--remove-folder` to delete QPSync too). No admin rights.

## Known limits in this build

- Windows client is built and packaged (tailscaled userspace + DPAPI + Scheduled Task) but has not been run on a
  Windows machine yet. Test on one before inviting Windows members.
- Resume of a single interrupted multipart upload across an agent restart depends on rclone; an upload interrupted
  mid-file restarts that file on the next cycle, other files are unaffected. (FR-37 partially met.)
- The macOS installer one-liner is the reliable path. The downloadable `.command` fallback is unsigned, so Gatekeeper
  asks the user to right-click and Open. Apple Developer ID signing would remove that step.
- Finder sidebar pinning uses a deprecated Apple API (`LSSharedFileList`) that still works on macOS 26; if Apple
  removes it, the folder still exists at `~/QPSync`, only the sidebar shortcut is lost.
