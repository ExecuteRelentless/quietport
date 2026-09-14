# 0020 A folder's name is a directory name, checked on the hub and again on every computer

**Context.** Whoever starts a folder names it: a member on the Share page, a new person at open signup, or the
operator with `qpctl`. Every member's computer then makes a directory of that name under the sync root and syncs it
in both directions with the folder's bucket. The hub stored the name as typed, and the agent only replaced the
characters Windows refuses. Nothing stopped a folder called `..`, which on each member's computer is the member's
home directory: the first sync would have sent their whole home directory to everyone in the folder, and the
folder's other members could have written into it. A folder called `.` was the sync root itself, with every other
folder on that computer inside it. Open signup takes a folder name from anyone who can reach the hub, and a member
can send a link to anyone. A read of the hub database on 2026-09-14 found no such name among the 21 folders.

**Decision.** One rule, `model.FolderName`, turns a typed name into the directory it will be: separators and the
characters Windows refuses become `-`, control characters go, dots and spaces at either end go, a Windows device
name such as `CON` or `LPT9.txt` gets `Folder ` in front, and the result is at most 40 characters, cut between
characters. A name with nothing left is refused. The hub applies it wherever a folder name is set (member start,
open signup, operator create and rename) and stores the result, so the name a member reads is the directory they
find. Every computer applies it again when it makes the directory, and falls back to `Shared` for a name with
nothing left, so a name that reached an older hub, or a hub that is wrong, still cannot leave the sync root.

Dropping leading dots also means no folder is hidden, and dropping trailing dots and spaces makes Windows and macOS
agree: Windows removes them on its own, so `Photos.` was already `Photos` there and a different directory on a Mac.

**Consequence.** No live folder changes directory: none of today's names contain anything the rule alters. A name
typed as `Summer: 2026.` is stored and shown as `Summer- 2026`. Because the hub protects computers the moment it is
redeployed, the agent's copy of the rule matters only against a hub that did not apply it; old agents are covered
by the hub alone until they update.

Tests that failed first: `TestFolderNamesThatAreNotNamesAreRefused` (hub: member start, signup, operator create and
rename all answered with storage errors or a 200 that stored `..`) and `TestAFolderNameNeverLeavesTheSyncRoot`
(agent: `..` resolved to the home directory, `.` to the sync root).
