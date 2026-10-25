#!/bin/sh
# Installs `mw`, the Master da Web CLI:
#
#   curl -fsSL https://raw.githubusercontent.com/masterdaweb/mw-cli/main/install.sh | sh
#
# Downloads the release for this system, checks it against the release's
# checksums.txt, and puts the binary in /usr/local/bin when that is writable
# (else ~/.local/bin). MW_VERSION=1.2.3 picks a version; MW_INSTALL_DIR a
# directory.
set -eu

REPO=masterdaweb/mw-cli
dir=${MW_INSTALL_DIR:-}

fail() {
  echo "mw install: $*" >&2
  exit 1
}

need() { command -v "$1" > /dev/null 2>&1 || fail "$1 is needed"; }
need uname
need tar
if command -v curl > /dev/null 2>&1; then
  get() { curl -fsSL "$1" -o "$2"; }
  latest() { curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest"; }
elif command -v wget > /dev/null 2>&1; then
  get() { wget -qO "$2" "$1"; }
  latest() { wget -S --spider "https://github.com/$REPO/releases/latest" 2>&1 | sed -n 's/^ *Location: *//p' | tail -1; }
else
  fail "curl or wget is needed"
fi

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "this script is for Linux and macOS; on Windows, download mw from https://github.com/$REPO/releases" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "no build for $(uname -m)" ;;
esac

version=${MW_VERSION:-}
if [ -z "$version" ]; then
  version=$(latest | sed 's#.*/tag/v##')
  [ -n "$version" ] || fail "could not find the latest release"
fi
version=${version#v}

archive="mw_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/v$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading mw $version for $os/$arch…"
get "$base/$archive" "$tmp/$archive" || fail "no release $version for $os/$arch"
get "$base/checksums.txt" "$tmp/checksums.txt" || fail "the release has no checksums"

want=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
[ -n "$want" ] || fail "$archive is not in checksums.txt"
if command -v sha256sum > /dev/null 2>&1; then
  got=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
else
  got=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)
fi
[ "$got" = "$want" ] || fail "the download does not match its checksum (got $got, want $want)"

tar -xzf "$tmp/$archive" -C "$tmp" mw

if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then dir=/usr/local/bin; else dir=$HOME/.local/bin; fi
fi
mkdir -p "$dir"
mv "$tmp/mw" "$dir/mw"
chmod 755 "$dir/mw"

echo "Installed $("$dir/mw" --version) in $dir."
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Add $dir to your PATH to run it as mw." ;;
esac
echo "Next: mw login (create a token in the console under Settings → API tokens)."
