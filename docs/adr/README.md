# Architecture decision records

One file per decision that is not obvious from the code. Newest last. Format: context, decision, consequences.
A decision is superseded by adding a new record, never by editing the old one.

- [0001 Invites are one-time links, not accounts](0001-invites-are-links.md)
- [0002 Circle keys never touch the hub](0002-keys-never-on-the-hub.md)
- [0003 Members start folders and invite from their own device](0003-members-start-and-share.md)
- [0004 A marker file keeps every folder syncable](0004-marker-file.md)
- [0005 Self-update resumes instead of timing out](0005-resumable-update.md)
- [0006 Members see the service, never the operator](0006-service-name-not-operator.md)
- [0007 No web viewer while the "server can never read it" promise stands](0007-no-web-viewer.md)
- [0008 The Windows installer downloads as a zip](0008-windows-installer-in-a-zip.md)
- [0009 The site welcomes search engines and AI crawlers](0009-site-welcomes-crawlers.md)
- [0010 tailscaled runs unelevated on Windows, so the Windows daemon is built with two flags](0010-windows-tailscaled-unelevated.md)
