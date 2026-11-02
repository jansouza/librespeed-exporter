#!/bin/sh
# Downloads a librespeed-cli release from GitHub, verifies it against the
# checksums.txt published with the release and extracts the binary and its
# license into DEST.
#
# usage: fetch-librespeed-cli.sh VERSION OS ARCH DEST
#   VERSION  release version, with or without a leading "v" (e.g. 1.0.14)
#   OS       linux, darwin or freebsd
#   ARCH     release architecture name (amd64, arm64, armv7, 386, ...)
#   DEST     directory to extract librespeed-cli and LICENSE into
set -eu

if [ "$#" -ne 4 ]; then
	echo "usage: $0 VERSION OS ARCH DEST" >&2
	exit 2
fi

version=${1#v}
os=$2
arch=$3
dest=$4

asset="librespeed-cli_${version}_${os}_${arch}.tar.gz"
base="https://github.com/librespeed/speedtest-cli/releases/download/v${version}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL --retry 3 -o "$tmp/$asset" "$base/$asset"
curl -fsSL --retry 3 -o "$tmp/checksums.txt" "$base/checksums.txt"

expected=$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")
if [ -z "$expected" ]; then
	echo "$asset is not listed in the release checksums" >&2
	exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$asset" | cut -d ' ' -f 1)
else
	actual=$(shasum -a 256 "$tmp/$asset" | cut -d ' ' -f 1)
fi
if [ "$actual" != "$expected" ]; then
	echo "checksum mismatch for $asset: expected $expected, got $actual" >&2
	exit 1
fi

mkdir -p "$dest"
tar -xzf "$tmp/$asset" -C "$dest" librespeed-cli LICENSE
echo "librespeed-cli $version ($os/$arch) extracted to $dest"
