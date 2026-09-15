# 0028 Every 30 seconds an agent asks whether a key waits for it, and heartbeats at once when one does

**Context.** Keys reach a device only in the bundle a heartbeat returns, and heartbeats are 5 minutes apart. When a
person joins a folder on one computer, that computer seals the key to the person's other computers at once (ADR
0019), and those computers then waited up to 5 minutes before their folder could sync; a key the operator grants
with `qpctl circle add-member` waited the same. The hub cannot reach a device: devices talk to the hub, never the
reverse. The plan offered two shapes: a short check-in when the hub knows a grant is waiting, or a 2-minute interval
fleet-wide. A 2-minute interval still leaves up to 2 minutes, and it stores 2.5 times the heartbeat rows (the hub
keeps 7 days of them per device) and asks the object store's admin API about every folder 2.5 times as often, all
the time, for an event that happens a few times a week.

**Decision.** `GET /v1/due`, device-authenticated, answers `{"heartbeat": true}` when a sealed grant was stored for
that device at or after its last heartbeat (`GrantWaiting`, from `key_grant.created_at` and
`device.last_heartbeat`, which both already existed). The agent asks every 30 seconds (`model.DueInterval`) from its
heartbeat loop, and `heartbeat` takes a lock, so a heartbeat the check brings forward never runs beside the regular
one or a retry. The times have one-second resolution, so a grant stored in the second of a heartbeat still counts:
one heartbeat too many rather than a key left for 5 minutes. Only grants count, not new memberships: a join adds the
membership a moment before the joining computer stores the grants, and a heartbeat in that gap would list the folder
without its key and tell the member the computer needs a new invitation. `TestADeviceIsToldToHeartbeatWhenAGrantWaitsForIt`
(hub) failed first with 404; `TestAnAgentReadsWhetherAKeyWaitsForIt` covers the agent's reading of the answer and
fails when the answer is ignored. The loop itself has no unit seam (it needs the mesh and the hub) and is proven live.

**Consequences.** A key sealed to a computer that is on reaches it within about 30 seconds, which the Share page, the
site's FAQ and `qpctl` now say. A heartbeat that fails after the hub recorded it does not lose the key: the agent
retries within 30 seconds, and every bundle carries all of the device's current grants. A folder removed, renamed or
re-keyed without a new grant for that device still arrives on the 5-minute heartbeat, and so does a new generation
whose grants were stored before the generation changed. An agent asking an older hub gets 404 and waits for its
heartbeat as before, so the hub can be deployed before or after the agents.
