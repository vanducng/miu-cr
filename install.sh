#!/bin/sh
# miucr installer — downloads the matching release archive, verifies its checksum,
# and installs the miucr binary to a writable bin dir (no sudo when avoidable).
#
#   curl -fsSL https://raw.githubusercontent.com/vanducng/miu-cr/main/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- v0.2.0      # pin a version
#
# Env knobs: MIUCR_VERSION (tag), MIUCR_INSTALL_DIR (target bin dir).
set -eu

REPO="vanducng/miu-cr"
BINARY="miucr"

info() { printf '%s\n' "$*" >&2; }
err() { printf 'error: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

VERSION="${MIUCR_VERSION:-${1:-latest}}"

detect_os() {
	os=$(uname -s | tr '[:upper:]' '[:lower:]')
	case "$os" in
		darwin) echo darwin ;;
		linux) echo linux ;;
		*) err "unsupported OS: $os (only darwin and linux are supported by this script; see README for Windows)" ;;
	esac
}

detect_arch() {
	arch=$(uname -m)
	case "$arch" in
		x86_64 | amd64) echo x86_64 ;;
		arm64 | aarch64) echo arm64 ;;
		*) err "unsupported architecture: $arch" ;;
	esac
}

# Releases publish darwin amd64+arm64, linux amd64 only. Guard the gaps.
check_supported() {
	os="$1"
	arch="$2"
	if [ "$os" = "linux" ] && [ "$arch" = "arm64" ]; then
		err "linux/arm64 is not published; build from source: go install github.com/$REPO/cmd/$BINARY@latest"
	fi
}

resolve_version() {
	archive="$1"
	if [ "$VERSION" != "latest" ]; then
		echo "$VERSION"
		return
	fi
	# Pick the newest release that ACTUALLY carries this archive, not just
	# releases/latest — there is a window after a tag is published where the
	# release exists but goreleaser hasn't uploaded its binaries yet, and the
	# asset download would 404. Walking the list (newest-first) and choosing the
	# first release whose assets include the archive skips that asset-less window.
	tag=$(download_stdout "https://api.github.com/repos/$REPO/releases?per_page=20" |
		awk -v want="$archive" '
			/"tag_name":/ { t=$0; sub(/.*"tag_name": *"/, "", t); sub(/".*/, "", t); cur=t }
			cur != "" && index($0, "\"" want "\"") { print cur; exit }
		')
	[ -n "$tag" ] || err "could not resolve a latest release containing $archive"
	echo "$tag"
}

# download_stdout fetches a URL to stdout. For api.github.com it sends a Bearer
# token when GITHUB_TOKEN/GH_TOKEN is set so the latest-release lookup isn't
# anonymous (unauthenticated requests hit a low rate limit → 403). The token is
# never printed. Unauthenticated still works, just rate-limited.
download_stdout() {
	url="$1"
	token="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
	authed=0
	case "$url" in
		https://api.github.com/*) [ -n "$token" ] && authed=1 ;;
	esac
	if have curl; then
		if [ "$authed" = 1 ]; then
			curl -fsSL -H "Authorization: Bearer $token" "$url"
		else
			curl -fsSL "$url"
		fi
	elif have wget; then
		if [ "$authed" = 1 ]; then
			wget -qO- --header="Authorization: Bearer $token" "$url"
		else
			wget -qO- "$url"
		fi
	else
		err "need curl or wget installed"
	fi
}

download_file() {
	url="$1"
	dest="$2"
	if have curl; then
		curl -fsSL -o "$dest" "$url"
	elif have wget; then
		wget -qO "$dest" "$url"
	else
		err "need curl or wget installed"
	fi
}

sha256_of() {
	if have sha256sum; then
		sha256sum "$1" | cut -d' ' -f1
	elif have shasum; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		err "need sha256sum or shasum to verify checksum"
	fi
}

pick_install_dir() {
	if [ -n "${MIUCR_INSTALL_DIR:-}" ]; then
		echo "$MIUCR_INSTALL_DIR"
		return
	fi
	# Prefer /usr/local/bin only when writable (no sudo); else ~/.local/bin.
	if [ -w /usr/local/bin ] 2>/dev/null; then
		echo /usr/local/bin
	else
		echo "$HOME/.local/bin"
	fi
}

main() {
	os=$(detect_os)
	arch=$(detect_arch)
	check_supported "$os" "$arch"

	archive="${BINARY}_${os}_${arch}.tar.gz"
	tag=$(resolve_version "$archive")
	base="https://github.com/$REPO/releases/download/$tag"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	info "Downloading $BINARY $tag ($os/$arch)..."
	download_file "$base/$archive" "$tmp/$archive"
	download_file "$base/checksums.txt" "$tmp/checksums.txt"

	info "Verifying checksum..."
	want=$(grep " ${archive}\$" "$tmp/checksums.txt" | cut -d' ' -f1)
	[ -n "$want" ] || err "checksum for $archive not found in checksums.txt"
	got=$(sha256_of "$tmp/$archive")
	[ "$want" = "$got" ] || err "checksum mismatch: expected $want, got $got"

	tar -xzf "$tmp/$archive" -C "$tmp"
	[ -f "$tmp/$BINARY" ] || err "archive did not contain $BINARY"

	dir=$(pick_install_dir)
	mkdir -p "$dir"
	install -m 0755 "$tmp/$BINARY" "$dir/$BINARY" 2>/dev/null ||
		{ cp "$tmp/$BINARY" "$dir/$BINARY" && chmod 0755 "$dir/$BINARY"; }

	info ""
	info "Installed $BINARY $tag to $dir/$BINARY"
	case ":$PATH:" in
		*":$dir:"*) ;;
		*)
			info ""
			info "Note: $dir is not on your PATH. Add it:"
			info "  export PATH=\"$dir:\$PATH\""
			;;
	esac
	info ""
	info "Next steps:"
	info "  $BINARY version"
	info "  export ANTHROPIC_API_KEY=...   # then: $BINARY review --staged"
	info "  Docs: https://miucr.vanducng.dev"
}

main "$@"
