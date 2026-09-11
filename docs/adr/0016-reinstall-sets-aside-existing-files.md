# 0016 A reinstall sets aside what it finds in a member's folder

**Context.** Every install marks each circle for resync, and a resync makes the folder and the hub's copy match in
both directions: anything held only on this computer is sent up and on to every other member. That is the one failure
mode in the product that reaches other people's computers rather than only the one in front of you. An installer can
land on a folder of the right name that this computer has no record of making: an uninstall that kept the folder, a
restored backup, a second person's leftovers, or the folder that 0.1.18's crypt remote filled with copies of
C:\Windows\System32 (docs/adr/0013). The content of such a folder is of unknown provenance, and the install has no
way to tell a member's own work from something that must never leave the machine.

**Decision.** Before the first sync, the installer moves what it finds in each folder into `Previous files <date>`
beside it in the sync root (`setAside` in `internal/agent/install.go`; `TestSetAside` failed before the change).
Quietport's own files stay where they are: the marker file and the versions folder are not the member's content, and
moving them would make every install look like a first one. A folder holding nothing but those is left alone rather
than gaining an empty set-aside folder. The set-aside folder sits in the sync root but outside every circle folder,
so nothing in it syncs anywhere.

An install that continues this computer's own device (a repair, an upgrade, the installer run a second time) leaves
its folders untouched. That folder is the synced copy by definition; moving it aside would make the member
re-download everything they already have, which is a worse failure than the one being guarded against.

**Consequence.** A member who uninstalls while keeping the folder and later reinstalls gets the hub's copy back and
finds their previous copy in `Previous files <date>` next to it. Anything that existed only on that computer is in
there, is sent to nobody, and can be moved back into the folder by hand, which syncs it deliberately rather than by
accident.
