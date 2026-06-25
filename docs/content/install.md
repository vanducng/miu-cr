---
title: Install
description: Install the miucr binary via install.sh, Homebrew, go install, or a Windows zip.
---

The binary is named `miucr`. Releases ship prebuilt static binaries for macOS (amd64 + arm64), Linux (amd64), and Windows (amd64). See [GitHub Releases](https://github.com/vanducng/miu-cr/releases).

## Install script (macOS / Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/vanducng/miu-cr/main/install.sh | sh
```

Pin a specific version:

```sh
curl -fsSL https://raw.githubusercontent.com/vanducng/miu-cr/main/install.sh | sh -s -- vX.Y.Z   # see github.com/vanducng/miu-cr/releases
```

The script detects your OS/arch, downloads the matching archive, **verifies its SHA-256 checksum** against `checksums.txt`, and installs `miucr` to `/usr/local/bin` when that is writable (no sudo) or `~/.local/bin` otherwise. It warns if the chosen directory is not on your `PATH`.

Knobs:

- `MIUCR_VERSION`: tag to install (same as the positional arg).
- `MIUCR_INSTALL_DIR`: target bin directory.

:::note
The script publishes darwin amd64+arm64 and linux amd64 only. On linux/arm64 it tells you to build from source (`go install …`).
:::

## Homebrew (macOS / Linux)

```sh
brew install vanducng/tap/miucr
```

The formula lives in [vanducng/homebrew-tap](https://github.com/vanducng/homebrew-tap) and is updated automatically on each release.

## go install (any platform with Go 1.25+)

```sh
go install github.com/vanducng/miu-cr/cmd/miucr@latest
```

:::tip
Make sure your Go bin directory is on `PATH`:

```sh
export PATH="$(go env GOPATH)/bin:$PATH"
```
:::

## Windows

Download `miucr_windows_x86_64.zip` from [Releases](https://github.com/vanducng/miu-cr/releases), extract `miucr.exe`, and put it on your `PATH` (e.g. a folder you add via *System → Environment Variables*).

In PowerShell:

```powershell
$version = "vX.Y.Z"   # see the Releases page for the latest tag
$asset = "miucr_windows_x86_64.zip"
Invoke-WebRequest "https://github.com/vanducng/miu-cr/releases/download/$version/$asset" -OutFile $asset
Expand-Archive $asset -DestinationPath ".\miucr" -Force
.\miucr\miucr.exe version
```

A Scoop manifest is planned.

## Update

Binaries installed via `install.sh` or the Windows zip self-update from the latest GitHub release:

```sh
miucr upgrade                  # download + install the latest release
miucr upgrade --check          # report whether a newer version exists, install nothing
miucr upgrade --version vX.Y.Z # install a specific release tag
```

Homebrew users update through Homebrew instead: `brew upgrade miucr`.

## Verify

```sh
miucr version            # stable JSON envelope
miucr version -o pretty
```

Then set a provider key and run your first review. See [Usage](/usage/) and [Credentials](/credentials/).
