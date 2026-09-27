#!/usr/bin/env bash
set -euo pipefail
# Run the upstream Linux test binary in-cluster, without exporting kubeconfigs.
root=$(cd "$(dirname "$0")/.." && pwd)
context=${KUBE_CONTEXT:?KUBE_CONTEXT required}
ns=lvmo-external-tests
k() { kubectl --context "$context" "$@"; }
cleanup() { k delete namespace "$ns" --ignore-not-found --timeout=120s; k delete clusterrolebinding "$ns" --ignore-not-found; }
trap cleanup EXIT
k create namespace "$ns"
k label namespace "$ns" pod-security.kubernetes.io/enforce=privileged
k -n "$ns" create serviceaccount runner
# Upstream e2e creates and inspects cluster-wide storage objects and namespaces.
k create clusterrolebinding "$ns" --clusterrole=cluster-admin --serviceaccount="$ns:runner"
version=${KUBERNETES_TEST_VERSION:-$(k version -o json | jq -r .serverVersion.gitVersion | sed -E 's/(v[0-9]+\.[0-9]+\.[0-9]+).*/\1/')}
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "Invalid Kubernetes test version: $version" >&2; exit 1; }
k -n "$ns" create configmap driver-config --from-file=driver.yaml="$root/tests/external-nfs.yaml" --from-literal=kubernetes-version="$version"
k -n "$ns" apply -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata: {name: runner}
spec:
  serviceAccountName: runner
  restartPolicy: Never
  containers:
  - name: runner
    image: ubuntu:24.04
    command: [bash, -ceu]
    args:
    - |
      apt-get update -qq
      apt-get install -y -qq curl ca-certificates
      case $(uname -m) in aarch64) arch=arm64;; *) arch=amd64;; esac
      version=$(cat /config/kubernetes-version)
      curl -fsSL https://dl.k8s.io/$version/kubernetes-test-linux-$arch.tar.gz | tar -xz -C /tmp kubernetes/test/bin/e2e.test
      /tmp/kubernetes/test/bin/e2e.test --storage.testdriver=/config/driver.yaml --ginkgo.focus='External.Storage.*lvmo.csi.io' --ginkgo.skip='\[Disruptive\]|\[Serial\]|\[Slow\]|performance|stress' --ginkgo.no-color --ginkgo.timeout=45m --report-dir=/reports
    securityContext: {runAsUser: 0}
    volumeMounts:
    - {name: config, mountPath: /config}
    - {name: reports, mountPath: /reports}
  - name: reports
    image: ubuntu:24.04
    command: [sleep, "3600"]
    volumeMounts:
    - {name: reports, mountPath: /reports}
  volumes:
  - name: config
    configMap: {name: driver-config}
  - name: reports
    emptyDir: {}
YAML
if [[ ${OPENSHIFT:-false} == true ]]; then
 oc --context "$context" adm policy add-scc-to-user privileged -z runner -n "$ns"
fi
k -n "$ns" wait --for=condition=Ready pod/runner --timeout=180s
k -n "$ns" logs -f runner -c runner
mkdir -p "$root/.test/reports/azure-external"
k -n "$ns" cp -c reports runner:/reports/. "$root/.test/reports/azure-external"
for attempt in $(seq 1 20); do
 result=$(k -n "$ns" get pod runner -o jsonpath='{.status.containerStatuses[?(@.name=="runner")].state.terminated.exitCode}')
 [[ -z $result ]] || break
 sleep 1
done
[[ $result == 0 ]]
