#!/bin/sh
# Install rex-rollcall from a GitHub release:
#   curl -fsSL https://github.com/danaimone/rex-rollcall/releases/latest/download/install.sh | sh
# Environment: VERSION=v0.5.0 (default: latest), BIN_DIR=~/.local/bin, BASE_URL=<mirror>.
set -eu

repo=danaimone/rex-rollcall
bin_dir=${BIN_DIR:-$HOME/.local/bin}

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "rex-rollcall: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "rex-rollcall: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

if [ -n "${BASE_URL:-}" ]; then
  base=$BASE_URL
elif [ -n "${VERSION:-}" ]; then
  base=https://github.com/$repo/releases/download/$VERSION
else
  base=https://github.com/$repo/releases/latest/download
fi

archive=rex-rollcall_${os}_${arch}.tar.gz
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

curl -fsSL -o "$work/$archive" "$base/$archive"
curl -fsSL -o "$work/checksums.txt" "$base/checksums.txt"

want=$(awk -v f="$archive" '$2 == f || $2 == "*" f { print $1 }' "$work/checksums.txt")
if [ -z "$want" ]; then
  echo "rex-rollcall: $archive is not in checksums.txt" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$work/$archive" | awk '{ print $1 }')
else
  got=$(shasum -a 256 "$work/$archive" | awk '{ print $1 }')
fi
if [ "$got" != "$want" ]; then
  echo "rex-rollcall: checksum mismatch for $archive (want $want, got $got)" >&2
  exit 1
fi

tar -xzf "$work/$archive" -C "$work" rex-rollcall
mkdir -p "$bin_dir"
install -m 0755 "$work/rex-rollcall" "$bin_dir/rex-rollcall"
"$bin_dir/rex-rollcall" version >/dev/null

echo "installed $bin_dir/rex-rollcall $("$bin_dir/rex-rollcall" version)"
echo "next: rex-rollcall install && rex config reload"
