# 0007 No web viewer while the "server can never read it" promise stands

**Context.** Phones and Chromebooks with no app would need a browser page served by the hub that decrypts and
encrypts in JavaScript.

**Decision.** Not built. A browser runs whatever code the hub serves at that moment, so a web viewer would let whoever
controls the hub steal keys or plaintext; the signed apps do not have that hole. The key would also live in browser
storage, exposed to extensions and shared profiles. This is a decision to revisit only with an explicit trust
statement on the site ("Apps: the server can never read your files. Browser: you trust the server's code each time").

**Consequences.** Mac, Windows and Linux only. If a viewer is ever built: enrolled as a removable device, upload but no
delete, strict CSP, no third-party scripts, and the trust difference stated plainly.
