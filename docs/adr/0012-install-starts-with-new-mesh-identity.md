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
