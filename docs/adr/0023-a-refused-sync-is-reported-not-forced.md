# 0023 A refused sync is reported, and a person chooses how it recovers

**Context.** When a bisync failed three cycles in a row with "Bisync aborted" in rclone's output, the agent forced a
full sync on the next cycle. A full sync decides, for every file that differs between the device and the hub, which
copy wins, and this one kept the device's copy. "Bisync aborted" covers far more than broken state. It covers rclone's
guard refusing a run in which every file changed, and errors rclone itself calls retryable, such as a dropped
connection. Proven with rclone 1.75.1: a member restores last week's copy of a folder, so every file has a new time,
while another member edits one file. rclone refuses the restorer's bisync, the third refusal forces the full sync, and
the other member's edit is replaced on the hub by the restored old copy. It survives only in the hub's hidden versions
folder. On 2026-09-14 the hub held 234 such forced full syncs across six folders. 221 were the marker loop 0.1.26 fixed
(docs/adr/0022), and one was in the folder of the only member from outside the operator's own circle.

**Decision.** The operator chose: force a full sync only when rclone says its saved state is unusable; otherwise report
and leave the files alone.

- The agent forces a full sync only when rclone's listings cannot be used and that cycle was not already a full sync
  (`afterFailedBisync`, `rcloneSaysResync`). rclone says so with "must run --resync" or a critical error it does not
  call retryable. It also counts when rclone "cannot find prior Path1 or Path2 listings": 1.75.1 calls that retryable
  under `--resilient`, but no later run finds them, and a crash in the middle of a sync leaves exactly that.
- When rclone's guard refuses a folder ("Safety abort") three cycles in a row, the folder is marked refused
  (`CircleState.Refused`) and reported once as `sync_refused:<slug>`. While it is refused, no full sync runs that a
  person did not choose: the agent's own scheduled full syncs, for a rename, a new key generation or lost listings,
  wait (`resyncMode`). The next sync that succeeds clears it.
- Any other failure three cycles in a row, such as a device with no connection, is reported once as
  `sync_failing:<slug>`. It is not refused and heals when it can.
- The device's status names a refused folder and gives the command that settles it, with the helper's full path.

A person settles a refused folder with `qpsync-agent resync <folder> --keep this|hub|newer`, run on that device. It
asks the running agent, through its loopback page, to make the folder's next sync a full sync. That sync keeps this
device's copies, the hub's, or the newer of each, where the sides differ (`requestFullSync`; `--resync`,
`--resync-mode path2`, `--resync-mode newer`). There is no default: the person names whose copies win. The request is
kept in the config until a sync succeeds, and a request made while another sync runs outlives that sync
(`syncSucceeded`). A folder that only receives, only sends, or is waiting for its key is refused with a sentence.
Outside a refusal, the agent's own full syncs keep this device's copies as before.

**Consequence.** A folder that rclone keeps refusing stops syncing on that device until someone looks, instead of
settling by overwriting the other members. That is worse for a member alone with the problem. The operator learns of
it from the one condition, and the runbook's "A folder that keeps refusing to sync" section says what to check and which
choice fits. A refused folder that is also renamed on the hub stays refused and unsynced until the person chooses.
There is no way yet to ask for the full sync from the hub or from the Share page.

Tests:
- Failed first: `TestAFailedSyncIsForcedOnlyWhenRcloneCannotUseItsListings`,
  `TestARefusedFolderTakesOnlyTheFullSyncAPersonChose` and `TestAFolderRefusedThreeTimesIsReportedNotForced`. The last
  runs the restore-and-edit story through rclone: refused five times and reported once; a scheduled full sync held
  while refused; the operator's hub-copies full sync settling it with the edit intact; then lost listings forcing a full
  sync that heals.
- With the refused hold taken out, that test fails with the edit replaced. Without recognising lost listings, it fails
  on the stuck cycle.
- `TestAFullSyncIsAskedForByFolderWithTheCopyToKeep`, `TestARequestMadeDuringAFullSyncOutlivesIt` and
  `TestStatusGivesARefusedFolderTheCommandThatSettlesIt` cover the request, the running-sync race and the status line.
- `TestAFullSyncAskedForFromTheCommandLineReachesTheRunningAgent` covers the command reaching the running agent. It was
  written after that endpoint and shown to fail without the endpoint and without starting the sync.
- Two reviews on two axes found the lost listings, the scheduled full syncs that ran while refused, the report on every
  cycle, and the offline device told to take a full sync.
