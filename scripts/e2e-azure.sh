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
# Transfer only this project's build into a private, short-lived blob container.
# Azure VM Run Command avoids opening SSH on the existing cluster subnet.
mkdir -p "$work/bin"
for cmd in lvmo-csi lvmo-driver lvmo-metadata; do CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$work/bin/$cmd" "./cmd/$cmd"; done
for suite in sanity integration; do CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o "$work/bin/$suite.test" "./tests/$suite"; done
tar -czf "$work/source.tgz" scripts tests -C "$work" bin
az group create -n "$rg" -l "$location" --tags project=lvmo-csi purpose=automated-test >/dev/null
created=true
account="lvmotest$(date +%s)"
az storage account create -g "$rg" -n "$account" -l "$location" --sku Standard_LRS --allow-blob-public-access false >/dev/null
az storage container create --account-name "$account" --name source --public-access off --auth-mode key >/dev/null
az storage blob upload --account-name "$account" --container-name source --name source.tgz --file "$work/source.tgz" --auth-mode key --overwrite >/dev/null
expiry=$(python3 -c 'from datetime import datetime,timedelta,timezone; print((datetime.now(timezone.utc)+timedelta(hours=2)).strftime("%Y-%m-%dT%H:%MZ"))')
sas=$(az storage blob generate-sas --account-name "$account" --container-name source --name source.tgz --permissions r --expiry "$expiry" --https-only --auth-mode key -o tsv)
az network nsg create -g "$rg" -n lvmo >/dev/null
az network nsg rule create -g "$rg" --nsg-name lvmo -n storage --priority 110 --source-address-prefixes "$cidr" --destination-port-ranges 2049 3260 50051 --access Allow --protocol Tcp >/dev/null
ssh-keygen -q -t ed25519 -N '' -f "$work/key"
az vm create -g "$rg" -n storage --image Ubuntu2404 --size Standard_D2s_v5 --admin-username lvmo --ssh-key-values "$work/key.pub" --subnet "$subnet" --nsg lvmo --public-ip-address "" --os-disk-size-gb 40 > "$work/vm.json"
private=$(jq -r .privateIpAddress "$work/vm.json")
step=0
remote() {
 local script=$1
 step=$((step+1))
 local job="lvmo-step-$step"
 # Managed Run Command uses the singular script field. This also avoids an
 # invoke --scripts serialization bug in Azure CLI 2.78.0.
 az vm run-command create -g "$rg" --vm-name storage --name "$job" --location "$location" \
  --script "$(cat "$script")" --timeout-in-seconds 5400 --async-execution false --output none
 az vm run-command show -g "$rg" --vm-name storage --name "$job" --expand instanceView --query instanceView > "$work/result.json"
 jq -r '.output, .error' "$work/result.json"
 [[ $(jq -r .exitCode "$work/result.json") == 0 ]]
 jq -r .output "$work/result.json" | grep -q '^LVMO_EXIT=0$'
}

[[ ${RELEASE_VERSION:-dev} =~ ^[A-Za-z0-9._-]+$ ]]
cat > "$work/setup.sh" <<SETUP
#!/bin/bash
set -e
curl -fsSL 'https://$account.blob.core.windows.net/source/source.tgz?$sas' -o /tmp/lvmo-src.tgz
mkdir -p /tmp/lvmo-src
tar -xzf /tmp/lvmo-src.tgz -C /tmp/lvmo-src
set +e
STORAGE_SERVER='$private' RELEASE_VERSION='${RELEASE_VERSION:-}' bash /tmp/lvmo-src/scripts/setup-vm.sh > /tmp/lvmo-setup.log 2>&1
result=\$?
tail -n 30 /tmp/lvmo-setup.log
echo LVMO_EXIT=\$result
SETUP
remote "$work/setup.sh"
# The transfer token is no longer needed once the VM has its source archive.
az storage blob delete --account-name "$account" --container-name source --name source.tgz --auth-mode key >/dev/null
unset sas
kubectl --context "$context" create namespace lvmo-system
installed=true
if [[ -z ${RELEASE_VERSION:-} ]]; then
 mkdir -p "$work/image"
 cp "$work/bin/lvmo-driver" "$work/bin/lvmo-metadata" "$work/image/"
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
 sanity)
  cat > "$work/suite.sh" <<'REMOTE'
#!/bin/bash
bash /tmp/lvmo-src/scripts/run-suites.sh sanity > /tmp/lvmo-sanity.log 2>&1
result=$?
tail -n 40 /tmp/lvmo-sanity.log
echo LVMO_EXIT=$result
REMOTE
  remote "$work/suite.sh";;
 snapshots) for sc in lvmo-nfs lvmo-iscsi; do STORAGE_CLASS="$sc" bash "$root/scripts/test-snapshots.sh"; done;;
 external) bash "$root/scripts/test-external-pod.sh";;
 metadata)
  cat > "$work/suite.sh" <<'REMOTE'
#!/bin/bash
CSI_ENDPOINT=unix:///tmp/lvmo-csi.sock /tmp/lvmo-src/bin/integration.test -test.v -test.timeout=15m > /tmp/lvmo-backup.log 2>&1
result=$?
cat /tmp/lvmo-backup.log
echo LVMO_EXIT=$result
REMOTE
  remote "$work/suite.sh"
  bash "$root/scripts/enable-metadata.sh"
  for mode in nfs iscsi-filesystem iscsi-block; do TEST_SOURCE_MODE="$mode" bash "$root/scripts/test-metadata.sh"; done;;
 esac
done
