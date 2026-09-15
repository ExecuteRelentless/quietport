# 0024 A heartbeat reports the path a ping to the hub took

**Context.** About 1 heartbeat in 6 across the fleet said `relayed` (3 days to 2026-09-15: one device 204 direct and
38 relayed, another 912 and 137), and the performance plan ranked "direct instead of relayed" as the biggest sync
lever. The report came from `tailscale status --json`: relayed when the hub peer had a DERP region and no `CurAddr`.
The daemon fills `CurAddr` only while it still trusts the peer's UDP path, which lapses a few seconds after the last
packet (`populatePeerStatus` and `addrForSendLocked` in tailscale 1.102.3), and the heartbeat read it before sending,
after up to 5 idle minutes. Sampling one device every 5 seconds for 8 minutes: the path never left direct, and 10 of
96 samples would have been reported relayed. The same device's ping to the hub answered `via 165.1.66.170:41641` on
the first try after 10 idle seconds.

What a relayed device costs, one 52 MB release file on 2026-09-15, same computer, same minute: 7.0 MB/s over the
internet, 5.6 MB/s over the mesh on a direct path, 1.45 MB/s through Tailscale's public San Francisco relay (a second,
throwaway tailscaled forced to relay with `TS_DEBUG_ALWAYS_USE_DERP`). A relayed device is about 4 times slower, and
rarer than the reports said: the real relayed episodes seen were a network that blocked UDP for 10 seconds
(`NetInfo ... udp=false`) and the seconds after waking from sleep.

**Decision.** `TS.HubPath` runs `tailscale ping -c 3 --timeout 3s <hub>`, and `pathFromPing` reports the path of the
last answer: `direct` for an address, `relayed` for `DERP(...)` or `peer-relay(...)`, `down` for no answer. A direct
path stops at the first answer, one round trip; a relayed one costs about 3 seconds of a 5-minute heartbeat, and no
answer about 9. `TestTheHubPathIsWhatAPingToTheHubTook` failed first, against outputs captured from tailscale 1.102.3.

**Consequences.** `relayed` in `qpctl status` now means a device went through a relay when asked, so a steady
`relayed` is worth a support question about the member's network (a VPN, a network that blocks UDP). A device whose
three pings all go unanswered reports `down` even if the heartbeat carrying that report then reaches the hub; that
takes a mesh failing for 9 seconds and recovering within the same heartbeat.
