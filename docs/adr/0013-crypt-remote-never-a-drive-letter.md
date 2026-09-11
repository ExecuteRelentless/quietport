# 0013 The crypt remote is never a drive letter, and the agent works from its own folder

**Context.** Every rclone call named the circle's crypt remote `C:`. On macOS and Linux that is a remote name. On
Windows rclone reads a single letter followed by a colon as a drive letter, so `C:` meant the current directory on
drive C. Task Scheduler starts the agent at logon with `C:\Windows\System32` as its working directory, so bisync paired
the member's circle folder with System32 instead of the hub. On 2026-09-11 the first Windows device to get past
enrolment (0.1.18, a member's laptop) filled its MKShared folder with 3.58 GB of copies of System32 within minutes.
Nothing reached the hub (the circle's bucket stayed at 42.1 MB) and System32 could not be changed (the agent runs
without administrator rights). Earlier Windows CI runs had reported "last sync ok" because the test started the agent
from a scratch directory, so bisync paired the folder with that directory: a local-to-local sync that looked like
success.

**Decision.** On Windows the crypt remote is named `QPCRYPT` (`cryptName` in `internal/agent/rclone.go`;
`TestCryptRemoteIsNotADriveLetter` failed before the change), and every rclone target and its environment prefix derive
from that name. macOS and Linux keep `C`: bisync keys its listing files by the remote name, and a rename would push
every running Mac into a resync. The agent also changes into its app folder before anything else, so no relative path
can resolve to System32 or to `/`. Windows CI proves a sync by what reaches the hub's bucket, never by the agent's own
"sync ok".
