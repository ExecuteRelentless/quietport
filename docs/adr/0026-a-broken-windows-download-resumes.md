# 0026 A broken Windows download resumes, because the zip is served as a view of the published exe

**Context.** The hub built the Windows zip on every request (ADR 0008): it read the 72 MB exe once for the CRC,
then streamed the zip. The plan said to build the generic zip once at publish instead. Measured on 2026-09-15, the
CRC read cost 10 to 90 ms of time to first byte (0.19 to 0.29 s against 0.18 to 0.21 s for the bare exe), which no
member notices. What they would notice: the zip answered `Range: bytes=1000000-1000099` with `200` and all
72,564,348 bytes, and sent no `Last-Modified`, so a browser could not resume a download that broke at 60 MB and
started over from zero, and nothing could cache it. A zip built at publish would fix only `/dl/Quietport-Windows.zip`:
the invite page's zip holds `Quietport-<code>.exe`, whose name carries the code, so it cannot be built ahead.

**Decision.** Both zips are one view over the published exe: the local header and the central directory are built in
memory (`zipAround`, split by count after flushing the header, so it does not depend on how `zip.Writer` buffers),
the entry's bytes are read from the file, and `http.ServeContent` answers `Range`, `If-Range` and `HEAD` against the
exe's modification time. The CRC is kept per file version, so the exe is read once per release, not once per
download. The bytes are unchanged: the view served locally over the published 0.1.28 exe, dated as on the hub, had
the same sha256 as the live zip (`7f7d14c6…`). `TestABrokenWindowsDownloadResumesWhereItStopped` failed first; it
also checks that a resume against a republished exe gets the whole new zip, never new bytes on an old start.

**Consequences.** Publishing a new exe changes its time, so a download in progress from the old one cannot be
completed with the new one. Serving is the same code for both zips, and nothing changes in `publish-release.sh`.
