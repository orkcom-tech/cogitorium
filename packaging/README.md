# Packaging

Every route installs the same binary. What differs is who fetches it and
whether the channel can bring Contextverse with it.

## Contextverse is a dependency, not a suggestion

Context and memory — an agent's own notes, shared workspace documents,
everything it is given to read — are stored and versioned by
[Contextverse](https://github.com/orkcom-tech/contextverse)'s `contextd`.
Without it the server starts, reports context as unavailable at
`GET /api/v1/context/status`, and memory does nothing.

Requirement 15 says Cogitorium installs together with Contextverse. Each
channel expresses that as strongly as it can, and no channel pretends:

| Channel | How Contextverse arrives |
|---|---|
| Homebrew | `depends_on "orkcom-tech/tap/contextd"` — brew installs it |
| Scoop | `"depends": "contextverse/contextd"` — scoop installs it |
| Docker | the image carries `contextd` and initialises its space on first start |
| deb / rpm | `Recommends: contextd`, and the postinstall prints the command. It cannot be `Depends`: contextd ships from GitHub releases, not from a distribution repository, so a hard dependency would make the package refuse to install |
| winget | declared under `Dependencies`, which winget records but does not resolve — the locale manifest says what to run |
| Archive / source | nothing fetches it for you; the server says so, and so does the documentation |

## Artifacts

Shipped by GoReleaser on every tag (`.github/workflows/release.yml`):

| Artifact | How |
|---|---|
| `.tar.gz` / `.zip` | archives, six targets (linux/darwin/windows × amd64/arm64) |
| `.deb` / `.rpm` | `nfpms` in `.goreleaser.yaml` |
| `checksums.txt` | plus a cosign signature and certificate |
| SBOM | one per archive |

## The desktop application

Built by `.github/workflows/desktop.yml` on native runners — macOS 14 for Apple
silicon, macOS 13 for Intel, windows-latest, ubuntu-22.04 — because a system
webview means cgo and cgo cannot be cross-compiled. The server's own release is
untouched by this: it stays pure Go and still cross-builds six targets from one
machine.

| Platform | Artifact | Window comes from |
|---|---|---|
| macOS | `Cogitorium_<v>_darwin_arm64.zip` / `_amd64.zip` — a `.app` bundle | WebKit |
| Windows | `Cogitorium_<v>_windows_amd64.zip` — `Cogitorium.exe` | WebView2 |
| Linux | `Cogitorium_<v>_linux_amd64.tar.gz` — binary, `.desktop`, icon, `install.sh` | WebKitGTK |

**Nothing is signed with a platform identity.** There is no Apple Developer
account and no Windows code-signing certificate for this project, so the first
launch is refused by Gatekeeper and warned about by SmartScreen. The macOS
bundle is ad-hoc signed — `codesign --sign -` — which is not a Developer ID and
does not get past Gatekeeper; it is there so an arm64 build is not reported as
*damaged*, which is what an entirely unsigned bundle produces. The documentation
says which click gets past each one. A signature that is not one would be worse
than the paragraph.

Contextverse is not bundled into the desktop builds. That is the one channel
where requirement 15 is unmet and says so rather than pretending: the app
reports context as unavailable and the documentation points at Homebrew, Scoop
or the Contextverse releases.

## Taps and buckets

| Manager | Repo | Install |
|---|---|---|
| Homebrew | `orkcom-tech/homebrew-tap` | `brew install orkcom-tech/tap/cogitorium` |
| Scoop | `orkcom-tech/scoop-bucket` | `scoop bucket add contextverse https://github.com/orkcom-tech/scoop-bucket` then `scoop install cogitorium` |
| Winget | [`winget/`](./winget/) templates | manual PR to `microsoft/winget-pkgs` when a release is cut |

`*.tmpl` files here are what `scripts/ci/publish-packages.sh` fills; the
non-template copies beside them are readable references for review, since a
formula full of `sed` placeholders is a formula nobody can check.

The hashes come from the release's own `checksums.txt` rather than from
re-downloading each archive, so a manifest can only ever describe artifacts
that release actually published. A missing checksum aborts the bump instead of
writing a manifest that lies.

## Publishing

```sh
PACKAGING_TOKEN=ghp_… ./scripts/ci/publish-packages.sh v0.1.0
```

`PACKAGING_TOKEN` needs `contents:write` on the tap and the bucket. Without it
the release still publishes and the bump is skipped with a message — a release
that cannot reach its package managers should say so, not half-succeed.
