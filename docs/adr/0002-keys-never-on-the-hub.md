# 0002 Circle keys never touch the hub

**Context.** The product promise is that the server cannot read anything, even if the VM is copied or the operator
turns bad.

**Decision.** Circle keys exist only on member devices (and in the operator keystore for circles the operator created
with `qpctl`). They move between devices in two sealed forms only: under the invite code (`SealWithCode`) and to a
device's x25519 public key (`SealToDevice`). A circle started by a member (installer or Share page) has its key
generated on that device; the hub makes the bucket and the record and never sees the key. Removing someone is a
rotation done from the owner's device: copy old generation to new under the new key, grant the new key to the
remaining devices, switch the generation, purge the old prefix.

**Consequences.** Loss of every device of a member-started circle loses that circle; there is no escrow by design.
`qpctl circle rotate-key` and `qpctl keys verify` do not apply to member-started circles. Backups of the hub are
ciphertext and safe to keep anywhere.
