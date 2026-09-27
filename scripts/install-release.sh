#!/usr/bin/env bash
set -euo pipefail
version=${1:?usage: install-release.sh vVERSION}
case $(uname -m) in aarch64|arm64) arch=arm64;; x86_64) arch=amd64;; *) exit 1;; esac
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"
base="https://github.com/michaelcourcy/lvmo-csi/releases/download/$version"
curl --fail --location --retry 3 -O "$base/SHA256SUMS"
for cmd in lvmo-csi lvmo-driver lvmo-metadata; do
 file="$cmd-linux-$arch"
 curl --fail --location --retry 3 -O "$base/$file"
 awk -v file="$file" '$2 == file' SHA256SUMS > "$file.sha256"
 test -s "$file.sha256"
 sha256sum --check "$file.sha256"
 install -m 0755 "$file" "/usr/local/bin/$cmd"
done
