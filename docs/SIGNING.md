# Windows code signing

**Status, 2026-09-14: unsigned.** SignPath Foundation declined the application for lack of public visibility (GitHub
stars, forks, contributors, outside references) and invited a new application once the project has more; SignPath
also offers a paid subscription. A certificate's common name must be the validated legal name (CA/Browser Forum Code
Signing Baseline Requirements 7.1.4.2.2), so a trade name cannot keep a person's name off the signature: only a legal
entity can. Options as of that date: Microsoft Artifact Signing (a paid Azure subscription, about $10 a month,
individual or organization, signs from GitHub Actions), Certum Open Source Code Signing (EUR 49, the developer's name),
SSL.com with eSigner, or an organization certificate from DigiCert or Sectigo. The workflow below takes any of them in
place of SignPath's signing step. Until then the invite page and the site's FAQ tell members how to get past the
warning, and `.github/workflows/defender-check.yml` checks a published installer against current Defender definitions.

## The free route that was tried: SignPath Foundation

SignPath Foundation issues and applies a code-signing certificate to open-source projects at no cost, on the condition
that the binaries are built by a public CI from a public repository. Quietport is MIT-licensed and builds on GitHub
Actions (`.github/workflows/windows.yml`), which is what they require.

## Apply (one time, by the maintainer)

1. https://signpath.org/apply (choose "Open Source"). Details to paste:
   - Project: Quietport, https://github.com/ExecuteRelentless/quietport, MIT.
   - Description: a private, invite-only shared folder for Mac, Windows and Linux. Files are encrypted on the
     member's computer before they leave it; the server stores ciphertext only.
   - Build: GitHub Actions, workflow `windows`, in two signing requests: artifact `windows-client-unsigned`
     (qpsync-agent.exe, qpctl.exe, Quietport Network.exe, tailscale.exe), then artifact `windows-installer-unsigned`
     (Quietport.exe, built around the signed client).
   - Maintainer: your name and email, GitHub user `ExecuteRelentless`.
2. When approved they create an organization for the project. In SignPath: add the GitHub repository as a trusted
   build system, create project `quietport` with signing policy `release-signing` (Authenticode, the OSS certificate),
   and an API token.
3. In the GitHub repository settings add secrets `SIGNPATH_API_TOKEN`, `SIGNPATH_ORGANIZATION_ID`, and variables
   `HUB_HOST` (`quietport.app` or the current hub name) and `RELEASE_PUBKEY` (the base64 line in
   `release-keys/release.pub`).
4. Push a tag. The workflow builds the client programs, SignPath signs them, the installer is built around the signed
   programs, and SignPath signs the installer. See "What is signed, in what order" and "Publishing a signed Windows
   release" below.

Turnaround for approval is typically days, not hours; the Foundation reviews each project by hand.

## What is signed, in what order

The installer carries the whole client inside it and installs those files, so signing a finished installer would
leave every program it installs unsigned, and so would the self-update bundle, which holds the same files. The
`windows` workflow therefore runs in this order:

1. `client`: builds `qpsync-agent.exe`, `qpctl.exe`, and tailscale (`Quietport Network.exe`, `tailscale.exe`) from
   source, and uploads them as `windows-client-unsigned`.
2. `sign-client`: SignPath signs them (`windows-client-signed`).
3. `installer`: adds `rclone.exe` (downloaded, checksum-pinned) and `VERSION`, zips the client into
   `quietport-windows-amd64-<v>.zip` (artifact `windows-client-bundle`, the self-update bundle), and builds
   `Quietport.exe` around that zip (`windows-installer-unsigned`). Once signing is configured it will not run around an
   unsigned client.
4. `sign-installer`: SignPath signs `Quietport.exe` (`windows-installer-signed`).

Before approval the two signing jobs are skipped and the installer is built around the unsigned client, so the
workflow still proves the build.

SignPath Foundation's terms decide what may carry the certificate. "The team must only sign software artifacts built
from their own source code". Upstream binaries may be included unsigned inside a signed package, and a modified upstream
build may be signed only "if the upstream project publishes signed builds". So:
- `rclone.exe` is never submitted. It is rclone's own release binary, and it ships unsigned inside the signed installer
  and bundle.
- tailscale is built from source here with two build flags (ADR 0010), and Tailscale publishes signed Windows
  builds. Confirm with SignPath when applying that this counts, or drop the two tailscale files from
  `windows-client-unsigned`.
- SignPath's artifact configuration can require consistent product name and version metadata on every signed file.
  Only `qpsync-agent.exe` and `Quietport.exe` carry a version resource today (`cmd/*/winres`); `qpctl.exe` and the
  tailscale files do not. Settle that in the artifact configuration, or add version resources, before the first run.

## Publishing a signed Windows release

Releases are assembled on the operator's Mac (docs/RUNBOOK.md, "Releases and self-update"), and the agents check the
release key's signature on each bundle, which CI cannot make. The Mac build takes the signed Windows files from the
tag's CI run instead of building unsigned ones:

```
gh run download <run id of the tag's windows workflow> -n windows-client-bundle -D /private/tmp/qp-win
gh run download <same run id> -n windows-installer-signed -D /private/tmp/qp-win
WINDOWS_CLIENT_ZIP=/private/tmp/qp-win/quietport-windows-amd64-<v>.zip scripts/build.sh <v>
WINDOWS_CLIENT_ZIP=/private/tmp/qp-win/quietport-windows-amd64-<v>.zip WINDOWS_INSTALLER=/private/tmp/qp-win/Quietport.exe \
  NOTARY_KEY=... NOTARY_KEY_ID=... NOTARY_ISSUER=... scripts/build-installer.sh <v> quietport.app
```

`build.sh` refuses a zip whose `VERSION` is not `<v>` or that lacks one of the five programs, uses the zip byte for
byte as the Windows bundle, and signs its checksum with the release key. `build-installer.sh` copies the signed
installer instead of building one, and only together with `WINDOWS_CLIENT_ZIP`, so the installer and the self-update
carry the same programs. Publish as usual.

## Paid alternatives, if faster matters

- Azure Trusted Signing: about $10 a month, identity validation once, signs from the CLI. Fastest.
- Certum Open Source Code Signing certificate: about 30 EUR a year.
