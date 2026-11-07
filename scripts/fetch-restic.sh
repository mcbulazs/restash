#!/bin/sh
# Downloads the pinned restic release for GOOS/GOARCH, checks it against
# scripts/restic.sha256, and puts it where the agent embeds it from
# (internal/restic/bundle/). Build the agent with -tags restic_bundled after.
#
#   scripts/fetch-restic.sh linux amd64
set -eu

os=${1:?GOOS}
arch=${2:?GOARCH}
root=$(cd "$(dirname "$0")/.." && pwd)
sums="$root/scripts/restic.sha256"
out="$root/internal/restic/bundle"
version=$(awk '$1 == "version" { print $2 }' "$sums")

# restic has no Windows ARM build; Windows on ARM runs the x64 one.
[ "$os" = windows ] && [ "$arch" = arm64 ] && arch=amd64

case $os in
windows) file="restic_${version}_${os}_${arch}.zip" ;;
*) file="restic_${version}_${os}_${arch}.bz2" ;;
esac
want=$(awk -v f="$file" '$2 == f { print $1 }' "$sums")
[ -n "$want" ] || { echo "no pinned checksum for $file" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$file" "https://github.com/restic/restic/releases/download/v${version}/${file}"
got=$(sha256sum "$tmp/$file" | cut -d' ' -f1)
[ "$got" = "$want" ] || { echo "checksum mismatch for $file" >&2; exit 1; }

mkdir -p "$out"
case $file in
*.zip) unzip -p "$tmp/$file" "restic_${version}_${os}_${arch}.exe" > "$tmp/restic" ;;
*) bunzip2 -c "$tmp/$file" > "$tmp/restic" ;;
esac
# Stored gzipped to keep the agent small; the agent checks this hash after
# unpacking.
sha256sum "$tmp/restic" | cut -d' ' -f1 > "$out/sha256"
gzip -9 -n -c "$tmp/restic" > "$out/restic.gz"
printf '%s\n' "$version" > "$out/version"
echo "bundled restic $version for $os/$arch"
