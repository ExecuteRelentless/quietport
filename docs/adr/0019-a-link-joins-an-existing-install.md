# 0019 A link opened on a computer that already has Quietport joins, it never reinstalls

**Context.** Every invite link minted a new person the moment it was made (`handleDeviceInvite` called `PersonAdd`),
and the only thing that could redeem it was a fresh install, which enrolled a new device of that person. A member
who already had Quietport and was sent a link had two bad paths: download 128 MB and install again, which ran
`agent.Install` with `c.Circles = nil` and threw away the config of every folder already on the computer, and
enrolled them a second time as a second person; or ask the inviter to do something the product had no way to do.
The installer's own "Use a new link" button on an installed computer led straight into the first path. A copy
change that told the invite page "already have Quietport? it appears on its own" was written, reviewed on both
axes, found to assert a mechanism that did not exist, and dropped on 2026-09-12; it was never pushed.

**Decision.** A link is the authorisation, for a join exactly as for an install: whoever holds the code can open
the keys sealed under it. So the hub gains `POST /v1/join`, device-authenticated, which consumes the code, makes the
calling device's person a member of the link's circles with the role the link carried, and returns the sealed keys,
the joined circles in the shape a heartbeat sends, and the ids and public keys of that person's other devices. It
returns nothing about anyone else. The person a member-made link minted, marked by the new `invite.minted_person`
column, is removed once the join has re-pointed the invite row at the person who redeemed it; an operator-made
link targets a person who existed before it and removes no one.

On the device, `joinCircles` folds the returned circles into the config in place: a folder that is missing is
added; a folder already there keeps its place and takes the hub's current settings, as a heartbeat would; a key
from the link replaces what the folder holds only when it holds none or the link's is newer, so a link sealed
before a re-key never downgrades a working folder; a key is stored for the generation it was sealed for; a resync
is scheduled only when the folder was added, had been removed, or its key changed; and a folder whose key did not
arrive is listed and marked, never dropped. The joining device then seals each key to
the person's other devices and stores the grants, which the hub now accepts from any member's device for that
person's own devices only (`handleCircleGrantsDevice`); before, only an owner re-keying could store grants, and a
second computer of a person who joined on their first would have reported that it needed a new invitation.

Members join from the Share page ("Have a link from someone?"). Every other entrance to an installed computer
leads there: the installer app hands the link to the running agent's Share page and shows the result; the Terminal
and PowerShell one-liners run `qpsync-agent join <code>` before they fetch anything, so the link is not consumed by
a script that could not use it; and `Install` itself refuses on a computer where `Installed()` is true, before it
reads the payload, so no path left or new can drop a computer's folders. Remove stays the way to start over. The
invite page describes the path in one sentence and promises nothing more.

Tests that failed first: `TestJoinAddsAnExistingPersonWithoutMintingOne`, `TestMemberDeviceGrantsOnlyToItsOwnComputers`,
`TestInvitePageTellsExistingMembersWhereToPasteTheLink` (hub), `TestJoinKeepsTheFoldersAlreadyOnThisComputer` and
`TestInstallRefusesToRunOverAnInstall` (agent). The two-axis review of the first cut found the key downgrade, the
unguarded shell path and the 200-with-nothing-joined answer; each got its failing case before its fix.

**Consequences.** A person with two computers who joins on one has the folder on both after the other's next
heartbeat. The hub still holds no circle key: what it relays is the blob the inviter sealed under the code and the
grants the joining device sealed. A member learns nothing new: the reply lists the folders the link was for, which
the invite page already showed them, and their own computers. A link redeemed by a join cannot also be installed
from, and the other way round; the existing consume-once row does both. A stale link, sealed before a re-key, joins
the person and leaves the computer waiting for a key, the same state a re-key leaves any device in, and the next
link heals it. Not done here: a URL scheme so that clicking the link opens the agent directly; the member pastes.
The mesh user and pre-auth key of a removed placeholder are cleared best effort; a failure there leaves an empty
Headscale user behind and is logged, which `qpctl` can tidy. Links made before this column existed carry
`minted_person = 0`, so a join within their 24 hours leaves the person they minted in place as a member with no
computer; the deploy that ships this marks the live member-made rows by hand (`UPDATE invite SET minted_person=1
WHERE consumed_at IS NULL AND revoked=0 AND inviter_name<>'<operator name>'`), and after 24 hours none remain.
Folder ids are sequential, so the grants endpoint, and `ownerCircle` with it, now answer a folder that does not
exist exactly as one the caller may not touch: 403, never 404.
