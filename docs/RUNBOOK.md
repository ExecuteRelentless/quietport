# Quietport operator runbook

This is the operator's reference for the running system as shipped (0.1.16). FR numbers refer to the product
specification, which is not published.

## The parts

| Where | What | How it runs |
|---|---|---|
| Hub VM | `qp-hub` on :443 (site, invites, downloads, TLS front for Headscale) and on `<tailnet ip>:8443` (agent + operator API) | systemd `qp-hub`, env in `/etc/quietport/hub.env` |
| Hub VM | Headscale 0.29 on 127.0.0.1:8080, policy pushed from `/var/lib/quietport/policy` (a git repo) | systemd `headscale` |
| Hub VM | Garage 2.2 object store, S3 API bound to the tailnet address only, one bucket per circle with a size quota | systemd `garage`, config `/etc/garage/garage.toml` |
| Hub VM | nightly encrypted backup (rclone crypt) to any S3-compatible bucket named in `/etc/quietport/backup.env`, weekly canary restore | `/etc/cron.d/quietport`, state in `/var/lib/quietport/backup/*.json` |
| Operator machine | `qpctl` + the keystore (`$QPCTL_HOME`, default `~/.config/quietport/`, sealed with the login Keychain on macOS) | run by hand |
| Each member | `qpsync-agent` + bundled `rclone` and userspace `tailscaled`, all under the user profile | LaunchAgent `app.quietport.agent` (macOS), Scheduled Task `Quietport` (Windows), systemd user unit (Linux) |

Circle keys exist only on member devices, plus the operator's keystore for circles the operator created with
`qpctl circle create`. Circles started by members (installer "Start a new folder", Share page "Start a new folder")
have their key on the founder's device and the devices it invited, nowhere else. The hub never has one. Loss of every
copy of a circle's keys makes that circle's files on the hub unrecoverable. There is no escrow, by design (FR-124).

## Day to day

```
qpctl status                      fleet: circles, people, devices, last heartbeats, backup state
qpctl status sam                one person, with per-circle sync times and conditions
qpctl logs sam --tail 50        heartbeat history + events for that person's devices
qpctl audit                       append-only audit log of every operator action
```

Conditions reported by agents (visible in `qpctl status`): `quota_exceeded:<slug>`, `path_too_long:<slug>:<n>`,
`corrupt_state:<slug>` (agent has scheduled a full resync), `disk_low`, `clock_skew`, and `relayed` when a device is
going through a DERP relay instead of a direct path (slower; usually a NAT or VPN on the member's side).

## Onboarding

```
qpctl person add sam --email sam@example.com --household home
qpctl invite create sam --circles circle-1,circle-2          # prints the link; works once; 24 h
```
Send the link however you normally talk to them. They paste one line, see `Quietport is connected.`, and the folders
appear. You see `invite.retrieved` then `device.enrolled` in `qpctl logs sam`. If the link expires or gets used
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

`QP_OPERATOR_NAME` is what members see for operator-made invites; keep it `Quietport` (the service), not a person.
`QP_SUPPORT_CONTACT` is only the Let's Encrypt account email; since 0.1.14 it is not sent to members or installers.

The invite page and the installer name the person who made the link ("Austin has shared the folder X with you";
0.1.14+, column `invite.inviter_name`), never the operator, unless the operator made the invite. People carry the
name they typed (`person.display_name`) next to the slug, and the Share page's people list shows that name.

The same page has **Start a new folder** (0.1.12+): a member types a name, the hub makes a bucket with `signup_quota`
bytes, storage credentials and the circle record with that person as owner, and the member's device generates the
circle key (the hub and the operator keystore never hold it, same caveat as open signup). The folder appears in QPSync
on that computer at once; the member then invites people to it from the picker at the top of the page. Off switch:
`qpctl settings member_circles=0`; cap per person: `max_member_circles=N` (default 10). Audit: `circle.create` by
`device:<id>/<name>`; events: `circle.created`. The operator can still create circles with `qpctl circle create`
(those keys do go into the keystore).


## Open signup (strangers can start their own folder)

`qpctl settings open_signup=1` lets anyone who has the installer (the public download) choose "Start a new folder",
type a name and a folder name, and get going without an invite. The hub creates the person (no email), a circle with
`signup_quota` bytes (default 5 GB), a mesh key, and the device generates the circle key itself. The founder can then
invite others from "Share a folder". `open_signup=0` turns it off; the installer then tells them to ask for a link.
Rate limit: 5 new folders per IP per hour. Every start is in `qpctl audit` as `signup.start` and in `qpctl events`.

Caveat: the operator's keystore never holds the key of a self-started circle, so `qpctl circle rotate-key` and
`qpctl keys verify` cannot act on it; offboarding a member of such a circle revokes and removes but skips rotation.

## Adding someone to another circle

```
qpctl circle add-member circle-1 alex          # key reaches Ben's devices on their next heartbeat (5 min)
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


## Folder owners removing people themselves

A circle can have an owner (`qpctl circle set <slug> --owner <person>`; self-started folders are owned by their
founder). The owner's Share a folder page lists the people in that folder with a Remove button. Removing someone
drops their membership on the hub (and their mesh devices, if that was their last folder), then the owner's own
computer re-keys the folder: new key, re-encryption through that computer, sealed grants for everyone still in, switch,
old ciphertext purged. Audit shows `circle.remove-member` and `circle.rotate-key` by `device:<id>/<owner>`.

After a device-driven rotation the operator keystore no longer has that folder's current key (`qpctl circle list`
shows `missing!`), so `rotate-key`, `keys verify` and offboard-driven rotation are done by the owner's computer for
that folder from then on, not by qpctl.

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
  MessageBox dialogs. Members download it inside `Quietport-<code>.zip`, which the hub builds on each request (ADR
  0008). The bare `.exe` URL still works. Unsigned (no Windows certificate): SmartScreen shows More info / Run anyway once.
- Linux (best effort): `quietport-installer-linux-<arch>`, client tarball embedded, prompts in the terminal, installs a
  systemd user unit. Run it as `./Quietport-linux-amd64` (no invite in the name: it asks for the link or offers to start a folder).
- Both still fall back to downloading the client bundle from `/dl/` if a build ships without the embedded files.
- Every folder carries a hidden `.quietport` file (0.1.14+). rclone bisync refuses to run against a prior listing with
  no files in it ("empty prior Path1 listing"), so a folder nobody has filled yet failed every cycle, counted as an
  error and would have raised the "not updated for a week" notice. The marker keeps listings non-empty; the agent also
  treats that abort as "run a full sync next" instead of a failure. If a member deletes the marker it comes back on the
  next cycle.
- Self-update (0.1.13+) downloads to `update-<version>.part` in the app dir, resumes with HTTP Range across heartbeats
  and gives up only after 3 minutes without a byte, so a slow or relayed link finishes over a few cycles. Agents on
  0.1.12 or older had a 90 s cap and cannot fetch a 50 MB bundle over a slow link: place the bundle by hand (copy
  `dist/<v>/client-<os>-<arch>/*` over the app dir, restart the agent) or re-run the installer.
- Build: `scripts/build.sh <v>` then `NOTARY_KEY=<AuthKey.p8> NOTARY_KEY_ID=<id> NOTARY_ISSUER=<issuer>
  scripts/build-installer.sh <v> <hub host>` (or `NOTARY_PROFILE=<notarytool keychain profile>`); check the DMG with
  `spctl -a -t open --context context:primary-signature -v <dmg>`, it must say "Notarized Developer ID". Publish with
  `deploy/publish-release.sh <v>` on the hub after copying everything in `dist/<v>/` to `/tmp` there.

## Releases and self-update

```
scripts/build-tailscale.sh windows amd64 ../vendor-bins/ts-windows-amd64   # only when tailscale changes; Windows daemon flags live here (ADR 0010)
scripts/build.sh 0.1.16           # hub, client bundles, operator kit; signs SHA256SUMS with ../release-keys/release.key
scripts/build-installer.sh 0.1.16 <hub host>   # Mac app + DMG (signed, notarized), Windows exe, Linux installers
scp dist/0.1.16/* <hub>:/tmp/
ssh <hub> sudo bash deploy/publish-release.sh 0.1.16   # installs nothing itself; registers the release for self-update
# a new hub binary: unpack quietport-hub-linux-<arch>.tar.gz and run deploy/hub-install.sh
```
Agents check for a newer version on every heartbeat, download it over the mesh, verify the sha256 and the ed25519
signature against the key compiled into them, swap binaries, restart, and roll back to the previous version if the
new one fails to start twice (`update.json` in the app dir records attempts). The release private key is
`../release-keys/release.key` on the operator's machine, outside the repository. Losing it means agents can never update
again without a reinstall; back it up with the circle keys.


## Windows: Defender and SmartScreen

The Windows installer and agent are unsigned Go binaries. Defender's heuristics can flag an unsigned program that
carries a compressed payload and registers a startup task, and SmartScreen shows "Windows protected your PC" until the
file has reputation. Since 0.1.11 the Windows binaries carry an icon, version information (company, product,
description) and an application manifest, and are not symbol-stripped, which removes the cheapest heuristic signals.
Two things finish the job, both outside this repo:

1. **Code signing.** Free for this project: SignPath Foundation signs open-source builds made by the public GitHub
   workflow (`docs/SIGNING.md`, `.github/workflows/windows.yml`). Paid routes if speed matters: Azure Trusted Signing
   (about $10/month, identity validation once) or a classic OV certificate from Certum or SSL.com.
   Sign `quietport-installer-windows-amd64.exe`, `qpsync-agent.exe`, `tailscaled.exe` and `rclone.exe` in the bundle
   with `signtool` (or the vendor's CLI) before publishing, and the SmartScreen prompt disappears once reputation builds.
2. **False-positive report to Microsoft.** https://www.microsoft.com/en-us/wdsi/filesubmission (sign in with a
   Microsoft account, "Software developer", upload the exe). Detections on clean files are usually lifted within 1 to
   3 days and the cleared hash stops being flagged for everyone.

Since 2026-09-10 the invite page and the site hand out the exe inside a zip (ADR 0008). That keeps a bare exe out of
the Downloads folder, but it is not a fix either: Defender scans archives and scans the exe again when it runs.

Until then, a member on Windows can use the paste-a-line path from the invite page's "Other options" (PowerShell
`irm … | iex`), which is not subject to the file download checks, or restore the file from Defender's quarantine and
add an allow entry. Both are workarounds, not the fix.

## Support call: what to ask a member to run

macOS: open Terminal and paste `"$HOME/Library/Application Support/Quietport/qp" status`
Windows: open PowerShell and paste `& "$env:LOCALAPPDATA\Quietport\qp.cmd" status`

It prints the hub contact time, connection type, and per-folder last sync + last error, with no file names.

Uninstall, 3 ways, all without admin rights and all leaving nothing behind (startup entry, app folder, keychain/DPAPI
entry, sidebar pin, the Share shortcut; the QPSync folder only if they say so):
- From the folder: open **Share a folder**, click "Remove Quietport from this computer".
- From the installer: open Quietport.dmg / Quietport.exe / the Linux installer again; it notices the install and offers Remove.
- From a terminal: the `qp` path above with `uninstall` (`--remove-folder` to delete QPSync too). Linux: `~/.local/share/quietport/qp uninstall`.

## Moving to a real domain

`scripts/wizard-domain-cutover.sh <domain> <ssh host> <version>` does it in 5 gated stages: register, DNS (apex and
`hub.` both DNS-only, the mesh control traffic cannot pass a proxy), hub env + headscale `server_url` + restart,
rebuild and publish installers with the new default host, verify. The old hostname stays in `QP_EXTRA_HOSTS` so
devices already enrolled keep working and pick up the new release on their own.

## Known limits in this build

- Windows client is built and packaged (tailscaled userspace + DPAPI + Scheduled Task) but has not been run on a
  Windows machine yet. Test on one before inviting Windows members.
- Resume of a single interrupted multipart upload across an agent restart depends on rclone; an upload interrupted
  mid-file restarts that file on the next cycle, other files are unaffected. (FR-37 partially met.)
- Phones and Chromebooks are not supported: there is no app for them and no web viewer (a web viewer would mean the
  hub serves code that touches keys, which is a different security model; see the README).
- The operator keystore has no key for circles members started themselves, so `qpctl circle rotate-key` and
  `qpctl keys verify` do not apply to those; the owner rotates from their own device.
- Updates and sync go through the mesh; a device shown as `relayed` (DERP) is slow, sometimes 100 KB/s. Direct paths
  need UDP 41641 open to the hub.
- Finder sidebar pinning uses a deprecated Apple API (`LSSharedFileList`) that still works on macOS 26; if Apple
  removes it, the folder still exists at `~/QPSync`, only the sidebar shortcut is lost.
