# 0004 A marker file keeps every folder syncable

**Context.** rclone 1.75 bisync aborts with "empty prior Path1 listing" when the previous run saw no files on either
side. Every new folder starts empty, so it failed every cycle, the error counter climbed, and after 7 days the agent
would have raised the "not updated for a week" notice.

**Decision.** The agent keeps a hidden `.quietport` file (83 bytes, hidden attribute on Windows) in every folder and
recreates it if deleted. It syncs like any file, so listings are never empty. Independently, the agent treats the
"empty prior listing" abort as "run a full sync next cycle" rather than a failure. The name must not start with
`.qp-`, which the default excludes drop.

**Consequences.** One extra hidden file per folder (invisible in Finder and Explorer by default). Existing empty
folders heal on the first cycle after the update.
