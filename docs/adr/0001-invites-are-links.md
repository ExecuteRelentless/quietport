# 0001 Invites are one-time links, not accounts

**Context.** Members are family and friends. Every account system (email, password, reset flow, 2FA) is a support
burden and a reason people stop using a tool. The threat model needs each device to be added on purpose, nothing more.

**Decision.** Membership is granted by a one-time link with a 26-character lowercase base32 code. The hub stores the
hash of the code, the circle keys sealed under a key derived from the code, and a 24-hour expiry. The device that opens
the link derives the key, enrols, and gets a bearer token of its own. No email is collected. Codes are lowercase
everywhere because the hub hashes the lowercased path.

**Consequences.** Nothing to reset and nothing to phish; a leaked link is dead after one use or 24 hours. A person is
identified by a slug and a typed display name only. Re-adding a lost laptop is "send a new link".
