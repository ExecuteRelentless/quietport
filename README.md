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

Build: `scripts/build.sh <version>` (Go 1.27, vendored rclone + tailscale binaries in `../vendor-bins`).
Licence: MIT for Quietport's own code. Bundled: rclone (MIT), Tailscale client (BSD-3), Garage (AGPL-3, server side
only), Headscale (BSD-3), Vault's shamir package (MPL-2.0, `internal/shamir/LICENSE`).
