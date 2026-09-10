# 0003 Members start folders and invite from their own device

**Context.** The operator cannot be a bottleneck for a private network among friends, and members must not need a
terminal, an email or an account to bring someone in.

**Decision.** Every synced folder carries a **Share a folder** shortcut to a loopback page served by the client. From
it a member makes a link (the key is sealed under the code on the member's device; the hub records the person,
circle, inviter and hash), starts a new folder (hub endpoint `POST /v1/circles`, key generated on the device, founder
becomes owner) and, as owner, removes people. Gating is by hub settings: `member_circles` (default on),
`max_member_circles` (10), `open_signup` and `signup_quota` for strangers with the installer.

**Consequences.** The operator sees everything in the audit log (`invite.create`, `circle.create` by
`device:<id>/<name>`) but does none of the work. Invite policy per circle can still restrict a circle to
operator-made invites (`qpctl circle set <slug> --invites operator`).
