# Quietport

A private, invite-only encrypted file network. Files appear in a normal folder. No accounts to log into, no interface
to learn, no server that can read the data. Specification: `../quietport-srd.pdf` (v1.0).

```
cmd/qp-hub          hub: invite service, agent + operator API, TLS front for Headscale, the public site
cmd/qpctl           operator CLI (the only admin interface)
cmd/qpsync-agent    member client: userspace tailscaled + rclone bisync per circle, heartbeat, self-update
internal/           model, hubdb (SQLite), cryptobox, cred (Keychain/DPAPI), agent, shamir (vendored, MPL-2.0)
deploy/             hub bootstrap + configure scripts, systemd units, headscale/garage configs, backup jobs
installers/mac      qp-sidebar (Finder sidebar helper, ObjC, universal)
scripts/build.sh    cross-builds everything and signs the bundles
docs/RUNBOOK.md     how to operate it
```


## Running your own hub (15 minutes)

1. A Linux VM (Ubuntu 24.04, 2 vCPU, 4 GB, arm64 or x86) with ports 22/tcp, 443/tcp and 41641/udp open, and a DNS
   name pointing at it. No domain yet? `<ip-with-dashes>.sslip.io` resolves to the IP for free and gets a real certificate.
2. Unpack `quietport-hub-linux-<arch>.tar.gz` on the VM, then as root:
   ```
   bash deploy/hub-bootstrap.sh                 # hardening + Headscale + Garage + rclone
   bash deploy/hub-install.sh                   # qp-hub, qpctl, backup jobs
   bash deploy/hub-configure.sh <your hostname> # configs, mesh, storage, TLS
   sudo -u quietport env $(cat /etc/quietport/hub.env | xargs) qp-hub init-operator   # prints your operator token
   ```
3. On your own computer, unpack the operator kit and connect over an ssh tunnel:
   ```
   ssh -N -L 18443:127.0.0.1:8443 <vm> &
   ./qpctl init --hub http://127.0.0.1:18443 --token <operator token> --operator <you>
   ./qpctl circle create family --name "Family"
   ./qpctl person add you && ./qpctl invite create you --circles family
   ```
   Open that link on your computer. Once you are a member, re-run `qpctl init` with `--hub http://<hub tailnet ip>:8443
   --proxy socks5://127.0.0.1:<socks_port from ~/Library/Application Support/Quietport/config.json>` and the tunnel is
   no longer needed.
4. Publish the client installers so members can be invited: build them with `scripts/build.sh` and
   `scripts/build-installer.sh` (Mac signing needs an Apple Developer ID), copy to the hub, `deploy/publish-release.sh`.

Everything else is in `docs/RUNBOOK.md`.

Build: `scripts/build.sh <version>` (Go 1.27, vendored rclone + tailscale binaries in `../vendor-bins`).
Licence: MIT for Quietport's own code. Bundled: rclone (MIT), Tailscale client (BSD-3), Garage (AGPL-3, server side
only), Headscale (BSD-3), Vault's shamir package (MPL-2.0, `internal/shamir/LICENSE`).
