#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
for tool in az kubectl oc helm ssh scp go jq; do command -v "$tool" >/dev/null; done
context=$(kubectl config current-context)
export KUBE_CONTEXT="$context" OPENSHIFT=true
(( $# )) || set -- sanity external snapshots metadata
# Refuse to overwrite installations or resources that this run does not own.
for resource in 'namespace/lvmo-system' 'csidriver/lvmo.csi.io' 'storageclass/lvmo-nfs' 'storageclass/lvmo-iscsi' 'volumesnapshotclass/lvmo-snapshots'; do
 if kubectl --context "$context" get "$resource" >/dev/null 2>&1; then echo "Already exists: $resource" >&2; exit 1; fi
done
provider=$(kubectl --context "$context" get nodes -o jsonpath='{.items[0].spec.providerID}')
[[ $provider == azure://* ]] || { echo 'Current cluster must run on Azure' >&2; exit 1; }
vmid=${provider#azure://}
subscription=$(az account show --query id -o tsv)
[[ $vmid == /subscriptions/$subscription/* ]] || { echo 'Cluster and active subscription differ' >&2; exit 1; }
nic=$(az vm show --ids "$vmid" --query 'networkProfile.networkInterfaces[0].id' -o tsv)
subnet=${AZURE_SUBNET_ID:-$(az network nic show --ids "$nic" --query 'ipConfigurations[0].subnet.id' -o tsv)}
location=$(az vm show --ids "$vmid" --query location -o tsv)
cidr=$(az network vnet subnet show --ids "$subnet" --query addressPrefix -o tsv)
[[ -n $cidr ]] || { echo 'Set a subnet with an IPv4 prefix' >&2; exit 1; }
work=$(mktemp -d)
rg="lvmo-test-$(date +%s)"
created=false
installed=false
cleanup() {
 status=$?
 trap - EXIT
 if [[ $installed == true ]]; then
  kubectl --context "$context" delete -f "$root/tests/storageclasses.yaml" --ignore-not-found || true
  helm uninstall lvmo --kube-context "$context" -n lvmo-system || true
  kubectl --context "$context" delete namespace lvmo-system --ignore-not-found --timeout=120s || true
 fi
 if [[ $created == true ]]; then az group delete --name "$rg" --yes --no-wait; fi
 rm -rf "$work"
 exit "$status"
}
trap cleanup EXIT
ssh-keygen -q -t ed25519 -N '' -f "$work/key"
adminip=${AZURE_ADMIN_CIDR:-$(curl -fsSL https://api.ipify.org)/32}
az group create -n "$rg" -l "$location" --tags project=lvmo-csi purpose=automated-test >/dev/null
created=true
az network nsg create -g "$rg" -n lvmo >/dev/null
az network nsg rule create -g "$rg" --nsg-name lvmo -n ssh --priority 100 --source-address-prefixes "$adminip" --destination-port-ranges 22 --access Allow --protocol Tcp >/dev/null
az network nsg rule create -g "$rg" --nsg-name lvmo -n storage --priority 110 --source-address-prefixes "$cidr" --destination-port-ranges 2049 3260 50051 --access Allow --protocol Tcp >/dev/null
az vm create -g "$rg" -n storage --image Ubuntu2404 --size Standard_D2s_v5 --admin-username lvmo --ssh-key-values "$work/key.pub" --subnet "$subnet" --nsg lvmo --public-ip-sku Standard --os-disk-size-gb 40 > "$work/vm.json"
ip=$(jq -r .publicIpAddress "$work/vm.json")
private=$(jq -r .privateIpAddress "$work/vm.json")
sshargs=(-i "$work/key" -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$work/known_hosts" -o ConnectTimeout=10)
for attempt in {1..30}; do if ssh "${sshargs[@]}" "lvmo@$ip" true; then break; fi; sleep 5; done
mkdir -p bin
for cmd in lvmo-csi lvmo-driver lvmo-metadata; do CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "bin/$cmd" "./cmd/$cmd"; done
for suite in sanity integration; do CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o "bin/$suite.test" "./tests/$suite"; done
tar --exclude=.git --exclude=.test --exclude=dist -czf "$work/source.tgz" .
scp "${sshargs[@]}" "$work/source.tgz" "lvmo@$ip:/tmp/lvmo-src.tgz"
ssh "${sshargs[@]}" "lvmo@$ip" 'mkdir -p /tmp/lvmo-src && tar -xzf /tmp/lvmo-src.tgz -C /tmp/lvmo-src'
# Arguments below are constrained Azure IPs and release tags, never shell text.
[[ ${RELEASE_VERSION:-dev} =~ ^[A-Za-z0-9._-]+$ ]]
ssh "${sshargs[@]}" "lvmo@$ip" "sudo env STORAGE_SERVER='$private' RELEASE_VERSION='${RELEASE_VERSION:-}' bash /tmp/lvmo-src/scripts/setup-vm.sh"
kubectl --context "$context" create namespace lvmo-system
installed=true
if [[ -z ${RELEASE_VERSION:-} ]]; then
 mkdir -p "$work/image"
 cp bin/lvmo-driver bin/lvmo-metadata "$work/image/"
 cat > "$work/image/Dockerfile" <<'DOCKER'
FROM ubuntu:24.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nfs-common open-iscsi util-linux e2fsprogs xfsprogs ca-certificates && rm -rf /var/lib/apt/lists/*
COPY lvmo-driver lvmo-metadata /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/lvmo-driver"]
DOCKER
 oc --context "$context" -n lvmo-system new-build --name=lvmo-csi --binary --strategy=docker
 oc --context "$context" -n lvmo-system start-build lvmo-csi --from-dir="$work/image" --follow --wait
 image_repository=image-registry.openshift-image-registry.svc:5000/lvmo-system/lvmo-csi
 image_tag=latest
else
 image_repository=michaelcourcy/lvmo-csi
 image_tag=$RELEASE_VERSION
fi
export TEST_IMAGE="$image_repository:$image_tag"
helm upgrade --install lvmo "$root/charts/lvmo-csi" --kube-context "$context" -n lvmo-system --set apiEndpoint="$private:50051" --set openshift=true --set image.repository="$image_repository" --set image.tag="$image_tag" --wait --timeout 5m
kubectl --context "$context" apply -f "$root/tests/storageclasses.yaml"
export API_ENDPOINT="$private:50051"
for suite in "$@"; do
 case $suite in
 sanity) ssh "${sshargs[@]}" "lvmo@$ip" 'sudo bash /tmp/lvmo-src/scripts/run-suites.sh sanity';;
 snapshots) for sc in lvmo-nfs lvmo-iscsi; do STORAGE_CLASS="$sc" bash "$root/scripts/test-snapshots.sh"; done;;
 external) bash "$root/scripts/test-external-pod.sh";;
 metadata)
  ssh "${sshargs[@]}" "lvmo@$ip" 'sudo env CSI_ENDPOINT=unix:///tmp/lvmo-csi.sock /tmp/lvmo-src/bin/integration.test -test.v -test.timeout=15m'
  bash "$root/scripts/enable-metadata.sh"
  for mode in nfs iscsi-filesystem iscsi-block; do TEST_SOURCE_MODE="$mode" bash "$root/scripts/test-metadata.sh"; done;;
 esac
done
