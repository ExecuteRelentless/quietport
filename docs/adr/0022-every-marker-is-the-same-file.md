# 0022 New markers are the same file on every device, and a refusal over the marker takes the hub's

**Context.** ADR 0004 put a hidden `.quietport` marker in every folder so bisync never sees an empty listing. Each
device wrote its own marker the first time it synced a folder, stamped with that moment. Every shared folder starts
with nothing in it but the marker. When a second member's device joined, its first sync was a full resync, and a full
resync keeps this device's copy wherever the two sides differ: it replaced the hub's marker with its own. On the first
member's device the folder's only file had now changed on the hub, and rclone refuses a bisync in which every file
changed ("Safety abort: all files were changed"), so that device stopped syncing the folder. New files are not
changes, so a member adding files did not help. After three refusals the agent ran a full resync, which put that
device's marker back on the hub and moved the refusal to the other device. That is the loop seen on 2026-09-12 and 13
in one of the operator's test folders, about one refusal a minute, until one of the two devices went offline.

Two designs were tried and dropped, both on evidence from rclone 1.75.1. Re-stamping existing markers to one time
failed because rclone counts a deleted file as a change: a re-stamp in the cycle that a member's delete of the
folder's last file reached a device was refused too, and the three-refusal resync then brought the file back. Making
every full sync keep the newer copy ended the loop between two updated devices. It did not end it while the device
still stuck was on 0.1.25: the updated device kept pushing its newer marker back over the older one. It also changed
what every full sync does to a member's file replaced by a copy with an older time.

**Decision.** Two parts.

1. A new marker has the same 83 bytes and the same modification time, 2020-01-01 00:00 UTC, on every device, so a
   folder started and joined on 0.1.26 never has two markers. A marker already in a folder keeps its content and time,
   because it is the hub's copy and changing it is a change bisync counts. It is hidden again on Windows, where a marker
   that arrived in a sync is an ordinary file.
2. When rclone refuses a bisync because every file changed, and every listing it kept from its last good run knew of
   nothing but the marker, the same cycle runs a full sync that keeps the hub's copy where the two sides differ
   (`--resync-mode path2`, `bisyncHealingTheMarker`). That cycle's result is the full sync's, so a healed folder is not
   a failure. The refusal can only be about the marker, and a full sync has nothing it could delete or bring back.
   Because the heal takes the hub's copy, a 0.1.26 device never pushes its own marker at anyone. With any file in the
   last listing the refusal stands, as rclone's guard against a folder whose every file was replaced.

Every other full sync is unchanged and keeps this device's copy.

**Consequence.** A folder shared before anything is put in it syncs on both devices. A pair left stuck by 0.1.25
settles within a few cycles once either device runs 0.1.26. That holds whether the stuck device's member added files
and whichever device updated first. A delete made just before the update still reaches the other device. A 0.1.25
device in a folder with a 0.1.26 device still takes its three refusals and its own full sync before it settles.

The three-refusal rule is untouched. After any three aborts it still runs a full sync that keeps this device's copy,
which can send a stale device's files over newer ones. That deserves its own decision.

Tests: `TestEveryDeviceWritesTheSameMarker`, `TestARefusalIsOverTheMarkerAloneOnlyWhenTheLastSyncKnewNothingElse`
and `TestARefusalOverTheMarkerAloneIsSettledInTheSameCycleKeepingTheHubsCopy` failed first.
`TestAMarkerFromTheHubIsHiddenOnWindows` runs on the Windows CI job. `TestTwoDevicesSyncASharedFolderThroughRclone`
runs two devices through rclone itself (`QP_RCLONE`); CI downloads a checksum-pinned 1.75.1 on Linux and Windows. A
0.1.26 cycle in it is the agent's own `bisyncHealingTheMarker` over `bisyncArgs`. It covers:
- a folder shared on 0.1.26
- a 0.1.25 device's folder joined from 0.1.26
- a stuck pair with nothing in the folder
- a stuck pair whose member added a file
- a stuck pair where only the device holding the newer marker updated
- a delete made just before the update

It fails in the matching scenario when any part is taken back: new markers stamped when written, no healing full
sync, a healing full sync keeping the newer copy, or one keeping this device's.
