# 0006 Members see the service, never the operator

**Context.** The invite page said "<operator> has shared a private folder with you" for every link, and installer
dialogs pointed to the operator's name and email. A member's friend saw the operator's name, which read as "shared
with the operator by default".

**Decision.** The hub records `invite.inviter_name` (the member's typed display name, or the operator name for
operator-made invites) and the page, the installer and the people list use it. `QP_OPERATOR_NAME` defaults to
`Quietport` and is the only name members see from the operator's side. `QP_SUPPORT_CONTACT` feeds the Let's Encrypt
account only and is not sent to members. Agent notifications say "ask the person who shared the folder with you".

**Consequences.** The operator's identity is not part of the member experience. The Apple Developer ID signature
still carries the signer's legal name; Gatekeeper does not show it for a notarized app.
