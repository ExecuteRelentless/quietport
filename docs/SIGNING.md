# Windows code signing (free route: SignPath Foundation)

SignPath Foundation issues and applies a code-signing certificate to open-source projects at no cost, on the condition
that the binaries are built by a public CI from a public repository. Quietport is MIT-licensed and builds on GitHub
Actions (`.github/workflows/windows.yml`), which is what they require.

## Apply (one time, by the maintainer)

1. https://signpath.org/apply (choose "Open Source"). Details to paste:
   - Project: Quietport, https://github.com/ExecuteRelentless/quietport, MIT.
   - Description: a private, invite-only shared folder for Mac, Windows and Linux. Files are encrypted on the
     member's computer before they leave it; the server stores ciphertext only.
   - Build: GitHub Actions, workflow `windows`, artifact `windows-unsigned` (Quietport.exe, qpsync-agent.exe,
     qpctl.exe, tailscaled.exe, tailscale.exe).
   - Maintainer: your name and email, GitHub user `ExecuteRelentless`.
2. When approved they create an organization for the project. In SignPath: add the GitHub repository as a trusted
   build system, create project `quietport` with signing policy `release-signing` (Authenticode, the OSS certificate),
   and an API token.
3. In the GitHub repository settings add secrets `SIGNPATH_API_TOKEN`, `SIGNPATH_ORGANIZATION_ID`, and variables
   `HUB_HOST` (`quietport.app` or the current hub name) and `RELEASE_PUBKEY` (the base64 line in
   `release-keys/release.pub`).
4. Push a tag (`git tag v0.1.12 && git push --tags`). The workflow builds, SignPath signs, and the signed files land in
   the `windows-signed` artifact. Publish them to the hub with `deploy/publish-release.sh` as usual.

Turnaround for approval is typically days, not hours; the Foundation reviews each project by hand.

## Paid alternatives, if faster matters

- Azure Trusted Signing: about $10 a month, identity validation once, signs from the CLI. Fastest.
- Certum Open Source Code Signing certificate: about 30 EUR a year.
