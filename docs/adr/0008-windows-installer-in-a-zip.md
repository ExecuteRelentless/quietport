# 0008 The Windows installer downloads as a zip

**Context.** The Windows installer is an unsigned Go exe that carries the whole client and registers a startup task.
On the maintainer's Windows machine Defender deleted it right after it was downloaded. Browsers and Defender are
harsher on a downloaded `.exe` than on an archive.

**Decision.** The invite page and the site link a zip: `/j/<code>/Quietport-<code>.zip` holding
`Quietport-<code>.exe`, and `/dl/Quietport-Windows.zip` holding `Quietport.exe`. The hub builds the zip on each
request from the published exe, stored rather than deflated, with the CRC and sizes in the local header. The invite
code still travels in the exe's own name, which is where the installer looks for it. The bare `.exe` URLs keep working
for links already sent.

**Consequences.** An extra step for the member: open the zip, then double-click Quietport inside. The zip does not
make the exe trusted. Defender scans archives too, and scans the exe again when it is extracted or run. SmartScreen
still asks once. Whether the zip alone gets the file past Defender on every Windows machine is not tested yet. Code
signing (`docs/SIGNING.md`) and a false-positive report to Microsoft remain the fix.
