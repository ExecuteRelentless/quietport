# 0021 Each folder has its own directory on a computer, and arrives there with nothing in it

**Context.** A circle's directory on a computer was `~/QPSync/<display name>`, worked out afresh wherever it was
needed. Names are not unique: the hub lets a person hold any number of folders called `Shared`, the name open signup
and the Share page suggest, and on 2026-09-14 two names already repeated among 21 folders. Since 0.1.25 a member
can join a friend's `Shared` from a computer that has its own. Both circles then synced one directory, each with its
own bucket, so each carried the other folder's files to its own members. A rename on the hub whose new name was
already a directory here had the same effect: the files stayed put and the folder pointed at the other directory.
Separately, ADR 0016 set aside what an install found in a folder's directory before the first sync, and nothing did
the same for a folder that reached an installed computer another way: a join, a heartbeat that adds or re-adds a
circle, or a member starting a folder. The first sync of such a folder is a full resync, which sends up whatever the
directory holds, and a removed folder leaves its directory in place.

**Decision.** Each circle on a computer records its directory, `CircleState.Folder`, chosen when the circle
arrives and kept after that; `CircleState.Dir` is the directory for sync, versions, watches, the marker, status, the
Share page's folder list and the join's answer. `Config.placeFolders` gives an arriving circle its name, or the first
free of `<name> 2`, `<name> 3`, among the live circles on that computer. Names are compared in Unicode's composed
form (NFC, which `model.FolderName` now produces) and without case, because macOS treats both differences as the same
name and Windows the second. Circles already here are placed before arrivals, so an arrival never takes a
directory from a folder that was here. A config from 0.1.25 records no directories, so its folders take their display
names on the first heartbeat; if two live ones share a name, the later moves. A removed circle holds no directory,
and one that comes back is placed again like any arrival. A name beginning with `Previous files` gets `Folder` in
front, because that is where set-aside files go and a folder there would sync them. A rename follows the new name
only when that name is free among live circles and no directory of that name exists on disk; otherwise the folder
takes the next free name, or stays where it is.

A directory that is new to its circle starts empty: every arrival, and a folder moved off a shared directory on
upgrade. `placeFolders` reports those circles and `emptyNewFolders` sets aside what their directories hold, apart
from Quietport's own files, into `Previous files <date>/<folder>`, or `<folder> 2` there when that day already used
the name, so a set-aside never overwrites an earlier one. Join, heartbeat, member start and install all go through
it under the config store's lock. A circle whose directory could not be emptied (a file held open on Windows, a
directory that cannot be made) keeps `AsidePending`, and the sync loop tries again before each sync and does not sync
that folder until it succeeds: a half-emptied directory would send the other half up. A folder that was already live
here is its synced copy and is never touched, which is ADR 0016's reasoning applied to every way a folder arrives.

Watches follow the live folders: a removed folder's watch stops only if no live folder has taken its directory, and
stopping `.../Shared` no longer stops `.../Shared 2`, whose path it prefixes.

**Consequence.** A member who joins a second `Shared` finds it in `~/QPSync/Shared 2`, and the Share page and the
join's answer use that name. Nothing moves on upgrade unless a computer already holds two live folders of one name;
then the later one moves to its own emptied directory and resyncs from its bucket, and what the shared directory
already held stays with the first. Install no longer creates folders from the invite's names before it enrols; it
makes them once the hub's bundle has placed them. The heartbeat path's wiring (`applyBundle`) and the retry in the
sync loop are covered by review and by a live check after publish, not by a unit test: `applyBundle` needs the OS
keychain.

Tests that failed first: `TestTwoFoldersWithOneNameGetTheirOwnDirectories` (and its Unicode and `Previous files`
cases), `TestAFolderArrivesWithNothingInIt` (and its failed set-aside case),
`TestAFolderMovedOffASharedDirectoryIsEmptiedLikeAnArrival`, `TestStoppingOneFoldersWatchLeavesTheOthers`.
`TestARenamedFolderNeverMovesIntoADirectoryThatIsTaken` was written after the rename code and shown to fail against
the old behaviour and against a version without the free-name search. The two-axis review of the first cut found the
failed set-aside, the upgrade move, the Unicode comparison, the unreserved set-aside name and the watches.
