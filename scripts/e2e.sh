#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
environment=local
if [[ ${1:-} == local || ${1:-} == azure ]]; then environment=$1; shift; fi
for suite in "$@"; do
 case $suite in sanity|external|snapshots|metadata) :;; *) echo "Usage: $0 [local|azure] [sanity external snapshots metadata]" >&2; exit 2;; esac
done
if [[ $environment == azure ]]; then exec bash "$root/scripts/e2e-azure.sh" "$@"; fi
for tool in limactl go tar; do command -v "$tool" >/dev/null; done
vm="lvmo-test-$(date +%s)"
mkdir -p .test/reports bin
created=false
cleanup() {
 status=$?
 trap - EXIT
 if [[ $created == true ]]; then
  limactl shell "$vm" sudo tar -czf /tmp/lvmo-reports.tgz -C /tmp/lvmo-src/.test reports 2>/dev/null || true
  limactl copy "$vm:/tmp/lvmo-reports.tgz" ".test/reports/$vm.tgz" || true
  if [[ ${KEEP_TEST_ENV:-false} == true ]]; then echo "Retained test VM: $vm";
  else limactl delete --force "$vm"; fi
 fi
 exit "$status"
}
trap cleanup EXIT
make build
for suite in sanity integration; do
 CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -o "bin/$suite.test" "./tests/$suite"
done
created=true
limactl start --name="$vm" --cpus=4 --memory=6 --disk=40 --mount-none --tty=false template:docker-rootful
archive=$(mktemp -t lvmo-src).tgz
tar --exclude=.git --exclude=.test --exclude=dist -czf "$archive" .
limactl copy "$archive" "$vm:/tmp/lvmo-src.tgz"
rm -f "$archive"
limactl shell "$vm" sudo mkdir -p /tmp/lvmo-src
limactl shell "$vm" sudo tar -xzf /tmp/lvmo-src.tgz -C /tmp/lvmo-src
limactl shell "$vm" sudo env RELEASE_VERSION="${RELEASE_VERSION:-}" bash /tmp/lvmo-src/scripts/setup-vm.sh
limactl shell "$vm" sudo env RELEASE_VERSION="${RELEASE_VERSION:-}" bash /tmp/lvmo-src/scripts/setup-kind.sh
limactl shell "$vm" sudo bash /tmp/lvmo-src/scripts/run-suites.sh "$@"
