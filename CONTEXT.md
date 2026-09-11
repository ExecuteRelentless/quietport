# Quietport

A private, invite-only shared folder: a hub on one small VM, a client on each member's computer, encryption on the
client, links instead of accounts. This file is the shared language for anyone (or any agent) working in the
repository. Use these terms exactly; the code, the runbook and the site use them.

## Language

**Hub**:
The one server (`cmd/qp-hub` plus Headscale and Garage on the same VM). It serves the site, invite pages and
downloads on :443, and the agent and operator APIs on the mesh address. It holds ciphertext, hashes and metadata,
never a circle key.
_Avoid_: server (in code and docs; "server" is fine on the public site), backend, control plane

**Circle**:
One shared folder: a Garage bucket with a quota, a key generation counter, a member list and an owner. `circle` is
the name in code, the database and `qpctl`; the site and the client call it a **folder**, because that is what
members see. A circle's folder on a member's computer is `~/QPSync/<display name>`.
_Avoid_: share, workspace, group, room

**Person**:
A member identity on the hub: a slug (`sam-k3q7`), the typed **display name** ("Sam"), a household, a mesh user, and
zero or more devices. A person has no password and no login. `Name` is the slug; `DisplayName` is what others see.
_Avoid_: user, account

**Device**:
One installed client, belonging to one person, with its own token, its own x25519 key pair for sealed grants, and its
own mesh node. Removing a device revokes its token and mesh node and re-keys the circles it was in.
_Avoid_: machine, node (node is the Headscale term; use it only when talking to Headscale)

**Member**:
A person who is in a given circle (a row in `membership`), with a role: `member` (read/write) or `readonly`.

**Owner**:
The person who founded a circle (`circle.owner_person_id`). Only the owner sees the people list and can remove
someone. The **operator** is a different role (below); an owner is an ordinary member with one extra power.

**Operator**:
Whoever runs the hub and uses `qpctl`. Members never see the operator's name; they see the service name, `Quietport`
(`QP_OPERATOR_NAME`). The operator can create circles, make invites, offboard people and rotate keys of circles the
operator created; the operator has no key for circles that members started.

**Invite** / **link**:
A one-time URL `https://<hub>/j/<code>` with a 26-character lowercase base32 **code**. The hub stores only
`HashToken(code)`, the invitee person, the circle ids, who made it (**inviter**), and the circle keys sealed under a
key derived from the code (`SealWithCode`). It works once and expires in 24 hours. Members make links from the
**Share page**; the operator from `qpctl invite create`.
_Avoid_: token (that is the device's bearer secret), invitation code without "invite"

**Share page**:
The local page the client serves on loopback (`127.0.0.1:<ui_port>/?t=<ui_token>`), reachable from the
**Share a folder** shortcut inside every synced folder. It makes links, starts new folders, shows the owner's people
list, and offers Remove Quietport.

**Open signup**:
Hub setting `open_signup=1`: anyone with the installer can choose **Start a new folder** without a link. The hub makes
the person, a circle of `signup_quota` bytes and a consumed invite; the device generates the key.

**Circle key** / **generation**:
The rclone crypt password and salt for a circle. Generation `g<N>/` is the object prefix in the bucket; a rotation
(owner removes someone, or `qpctl circle rotate-key`) creates generation N+1, re-encrypts, grants the new key to the
remaining devices and purges the old prefix.

**Sealed grant**:
A circle key sealed to one device's x25519 public key (`SealToDevice`), stored on the hub, opened only by that device.
This is how a key reaches a device after enrolment (rotation, added to another circle).

**Bundle** (config bundle):
What the hub returns on every heartbeat: the circles the device is in (with S3 credentials, generation, owner and
`can_invite` flags), the update pointer, and settings. The agent applies it (`applyBundle`): adds, renames, removes
folders, fetches grants, schedules resyncs.

**Heartbeat**:
The agent's 5-minute report to the hub: version, connection type (direct / relayed), free disk, per-circle sync
health. The hub answers with the bundle.

**Release** / **self-update**:
A signed client bundle per OS/arch registered on the hub (`qp-hub release add`, `deploy/publish-release.sh`). Agents
download it to `update-<version>.part` (resumable), verify sha256 + ed25519 against the compiled-in release public key,
swap binaries, restart, roll back after 2 failed starts.

**Marker** (`.quietport`):
A hidden file the agent keeps in every folder so rclone bisync never sees an empty listing.

**Installer**:
`cmd/qp-installer`: the Mac app in a notarized DMG, the single Windows exe (downloaded inside a zip), the Linux binary. The client is embedded;
the invite code comes from the file name, the app folder name, or a pasted link. It also removes an install.

**Mesh** / **tailnet**:
The Headscale-controlled WireGuard network. The hub is `100.64.0.1`, tagged `tag:hub`; every device runs userspace
tailscaled with a local SOCKS5 proxy that rclone and the agent use.

**Mesh daemon**:
The userspace tailscaled every device runs. On Windows the file is named `Quietport Network.exe`: the Firewall prompt
a member answers names the file that listens, and that is the only place the name is ever seen.
_Avoid_: tailscaled in anything a member reads

**Mesh identity**:
The keys that identify one device on the mesh. Every install creates a new one, so a new install never inherits an
earlier attempt's.
_Avoid_: machine key, node key (Headscale's terms; use them only when talking to Headscale)

**Mesh user**:
A person's identity in Headscale, holding the mesh nodes of all that person's devices.
_Avoid_: user on its own

## Relationships

- A **hub** has many **persons**, **circles** and **devices**.
- A **person** has many **devices** and many **memberships**; a **circle** has many **members** and one **owner**.
- A **person** has one **mesh user**; a **device** has one **mesh identity**, new with every install.
- An **invite** targets one **person** and one or more **circles**, records one **inviter**, and is consumed once.
- A **device** holds one **sealed grant** per **circle** and **generation** it may open.
- The **operator** is a **person** only if they enrol a device of their own; `qpctl` needs no membership.

## Flagged ambiguities

- "folder" vs "circle": resolved. Members read **folder**; code, database, `qpctl` and this file say **circle**.
- "operator" vs "owner": resolved. Different roles; the site never mentions either, it says "the person who shared
  the folder with you".
- "token": resolved. A **device token** is the bearer secret; an invite has a **code**.
- "user": never. It is a **person** on the hub, a **member** in a circle, a **device** on a computer, a **mesh user**
  in Headscale.
- "account" / "standard user": resolved. Windows' words for a login on a computer, with or without administrator
  rights; use them only when talking about Windows permissions, never for a person.
