# 0014 On Windows tailscaled sends DNS queries without EDNS

**Context.** On Windows tailscaled resolves the control server's name with Go's own DNS client
(`net/dnscache`: `preferGoResolver()` is true everywhere except macOS, iOS and Android, and nothing changes it), and
Go adds an EDNS0 record to every query. Some DNS forwarders mangle the reply to such a query. VMware Fusion's NAT DNS
on macOS (vmnet, `192.168.120.2` in the maintainer's VM) answers an EDNS query with a malformed packet (an OPT record with
must-be-zero bits set, 16 extra bytes at the end) and refuses TCP, yet answers the same query without EDNS correctly.
Go's parser rejects the malformed reply, and Tailscale's bootstrap-DNS fallback only serves Tailscale's own names, so
`fetch control key` never left the machine: `tailscale up` hung until the agent's 60 s timeout killed it (Windows
reports that as `exit status 1` with nothing on stderr), and every install in that VM failed from 0.1.15 on, while the
installer, which uses Windows' own resolver, reached the hub. Go documents a setting for exactly this: EDNS0 "can
reportedly cause sporadic failures with the DNS server run by some modems and routers. Setting GODEBUG=netedns0=0 will
disable sending the additional header."

**Decision.** The agent starts tailscaled on Windows with `netedns0=0` added to `GODEBUG` in its environment, keeping
any setting already there (`tsEnv` in `internal/agent/tailscale.go`; `TestTailscaledEnvDisablesEDNSOnWindows` failed
before the change). No Tailscale source patch. macOS uses the system resolver and is unchanged; Linux is left as is.
Without EDNS a UDP answer is capped at 512 bytes; the names tailscaled resolves (the control server, DERP servers)
have small answers.
