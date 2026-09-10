# 0005 Self-update resumes instead of timing out

**Context.** The hub client had one 90-second request timeout for everything, including a 52 MB update, so a device
on a slow or relayed link could never update ("update download: context deadline exceeded" every heartbeat).

**Decision.** Updates download to `update-<version>.part` with an HTTP `Range` resume across heartbeats, no total
deadline, and a 3-minute stall watchdog. The hub serves updates with `http.ServeFile`, which honours ranges. The hub
compares against the version the agent reports in the current heartbeat, and the agent never fetches its own version.

**Consequences.** A slow device finishes over a few cycles. Devices on 0.1.12 or older cannot get past the old cap on
a slow link and need the bundle placed by hand once.
