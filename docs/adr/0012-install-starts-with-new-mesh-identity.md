# 0012 Every install starts with a new mesh identity

**Context.** tailscaled keeps its machine key, node key and login profiles in its state directory (`ts/` in the app
folder). A failed install leaves that directory behind, and so does an earlier finished one. The next install started
tailscaled on the old state, and `tailscale up --auth-key` re-registered the old node key under the new invite's user.
Headscale then held two nodes with the same machine, node and disco keys ("Creating new node for different user (same
machine key exists for another user)"), the hub's own tailscaled saw one peer key for two nodes ("disco key changed",
"derp does not know about peer ... removing route"), and traffic from the new node never reached the hub API: every
enrol attempt timed out and the install reported "the network accepted this computer but the hub did not." Seen on a
member's Windows laptop on 2026-09-11: a 0.1.16 attempt failed, and the 0.1.17 retry with a new invite failed this way
(headscale nodes 15 and 16, identical keys). CI never saw it because every runner starts with no state.

**Decision.** Install removes the state directory before it starts tailscaled, after any running agent is stopped
(`resetMeshState` in `internal/agent/install.go`; `TestResetMeshState` failed before the change). Every install
registers a new node with new keys. If the directory cannot be removed, the install stops with "the previous network
settings on this computer could not be cleared." rather than register under the old identity.

**Consequences.** Verified end to end (branch `diag/windows-reinstall`, run 34638253358): on one standard Windows
account, install once with the published 0.1.17 client, then again with a new invite. With the 0.1.17 agent the
second install re-registered the first node key (both registrations `node=[21QBs]`, headscale nodes 19 and 21
sharing one key) and failed after 75 s with "the network accepted this computer but the hub did not". With this
change it generated a new node key, installed in 5 s, and the hub answered through the SOCKS proxy 40 s later. A node
an earlier attempt left behind stays in Headscale as an offline node of its own user until the operator removes it;
it no longer shares keys with anything. Self-update does not go through Install, so a running device keeps its
identity across updates. The test harness must run Install under the installer's name: Install stops an earlier agent
with `taskkill /IM qpsync-agent.exe`, which kills a caller running under that name.
