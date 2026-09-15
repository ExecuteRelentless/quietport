# 0025 The hub does not run its own relay, because it measured no faster

**Context.** Every device's only mesh peer is the hub, and Headscale can serve a DERP relay itself. A device that
cannot reach the hub over UDP would then relay through the hub instead of Tailscale's public relays, which sounded
like the fix for relayed sync speed (ADR 0024 measured 1.45 MB/s relayed against 5.6 MB/s direct).

It was tried live on 2026-09-15: Headscale's embedded DERP server as the mesh's only region, STUN on 3478/udp opened in
the security group and iptables, and the hub's own public address confirmed first (a hairpinned connection from the
hub to its public IP arrives from that IP, so the hub's STUN answer is right). The throwaway tailscaled forced to relay
pinged `via DERP(quietport)` and fetched the same 52 MB file at 1.15 and 1.0 MB/s, while the internet path gave
10.4 MB/s in the same minute. The relay's location was not the limit; relaying is (WireGuard inside TLS over one TCP
connection, and a relay write queue that drops packets when it fills, which the tunnelled TCP reads as loss).

**Decision.** The hub keeps Tailscale's public relay map. The trial was reverted the same morning: the Headscale
config, its relay key file, the iptables rule and the security-group rule.

**Consequences.** Relayed devices stay about 4 times slower than direct ones, and the fix for one is its network (UDP
41641 to the hub). Running the relay on the hub would take a third party's relays out of the mesh, which is a privacy
decision, not a performance one, and is left for its own record. Changing the relay map has two traps, both hit
during the trial and written into the runbook: the hub's daemon keeps a stale home region until it re-runs STUN,
and it chose Paris for 4 minutes after the revert because tailscale keeps the best latency per region from the last
5 minutes of reports, one of them taken mid-change; and `headscale configtest` run as root creates a missing key
file that the `headscale` system account cannot read, so the service does not start.
