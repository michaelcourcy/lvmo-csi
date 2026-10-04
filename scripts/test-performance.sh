#!/usr/bin/env bash
set -euo pipefail
# Runs the same fio jobs against each StorageClass, one class at a time, and
# prints a Markdown table. With REPLICAS > 1, that many pods, each with its
# own PVC, run the jobs at the same moment and the table shows totals.
# Usage: test-performance.sh STORAGECLASS...
# Env: KUBE_CONTEXT
#      NODE        hostname to run on (default: first schedulable node), or
#      NODE_LABEL  key=value selecting the nodes; pods spread across them and
#                  tolerate their taints
#      REPLICAS    parallel pods per class (1)
#      SIZE        PVC size (10Gi); FILE_SIZE fio file per pod (4g)
#      RUNTIME     seconds per job (60); START_DELAY seconds before the common
#                  start (0 for one pod, 300 otherwise); PREFILL_WINDOW seconds
#                  from that start until the measured jobs, which must cover
#                  every pod's prefill (0 for one pod, 600 otherwise)
#      OUT         raw fio JSON (.test/reports/perf): OUT/<class>.json, or
#                  OUT/<class>/<n>.json with replicas
(( $# )) || { echo "usage: $0 STORAGECLASS..." >&2; exit 2; }
root=$(cd "$(dirname "$0")/.." && pwd)
context=${KUBE_CONTEXT:-$(kubectl config current-context)}
replicas=${REPLICAS:-1}
size=${SIZE:-10Gi}
file_size=${FILE_SIZE:-4g}
runtime=${RUNTIME:-60}
delay=${START_DELAY:-$(( replicas > 1 ? 300 : 0 ))}
window=${PREFILL_WINDOW:-$(( replicas > 1 ? 600 : 0 ))}
out=${OUT:-$root/.test/reports/perf}
ns=lvmo-perf
k() { kubectl --context "$context" "$@"; }
if [[ -n ${NODE_LABEL:-} ]]; then
 placement="nodeSelector: {${NODE_LABEL%%=*}: \"${NODE_LABEL#*=}\"}
  tolerations: [{operator: Exists}]
  topologySpreadConstraints: [{maxSkew: 1, topologyKey: kubernetes.io/hostname, whenUnsatisfiable: DoNotSchedule, labelSelector: {matchLabels: {app: lvmo-fio}}}]"
 where="nodes $NODE_LABEL"
else
 node=${NODE:-$(k get nodes -o jsonpath='{range .items[?(@.spec.unschedulable!=true)]}{.metadata.name}{"\n"}{end}' | head -1)}
 # A node selector, not nodeName: WaitForFirstConsumer classes (EBS) only
 # provision once the scheduler has chosen the node.
 placement="nodeSelector: {kubernetes.io/hostname: $node}"
 where="node $node"
fi
mkdir -p "$out"
trap 'k delete namespace "$ns" --ignore-not-found --wait=true --timeout=600s >/dev/null' EXIT
k create namespace "$ns" >/dev/null
k -n "$ns" label namespace "$ns" pod-security.kubernetes.io/enforce=privileged --overwrite >/dev/null
# Each pod first writes its whole file (prefill, not measured): a block never
# written reads as zeros without touching the disk on thin LVs and new EBS
# volumes alike, and a first write costs allocation work. The measured jobs
# then run alone (stonewall), on real data, after a common start time.
k -n "$ns" create configmap fio-jobs --from-literal=prefill.fio="[prefill]
directory=/data
filename=fio.dat
size=$file_size
fallocate=none
direct=1
ioengine=libaio
rw=write
bs=1m
iodepth=16
" --from-literal=jobs.fio="[global]
directory=/data
filename=fio.dat
size=$file_size
fallocate=none
direct=1
ioengine=libaio
time_based=1
runtime=$runtime
ramp_time=10
group_reporting=1
stonewall
[seq-write-1m]
rw=write
bs=1m
iodepth=16
[seq-read-1m]
rw=read
bs=1m
iodepth=16
[rand-write-4k]
rw=randwrite
bs=4k
iodepth=32
[rand-read-4k]
rw=randread
bs=4k
iodepth=32
[rand-read-4k-qd1]
rw=randread
bs=4k
iodepth=1
" >/dev/null
for sc in "$@"; do
 start=$(( $(date +%s) + delay ))
 for n in $(seq 1 "$replicas"); do
  k -n "$ns" apply -f - >/dev/null <<YAML
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: perf-$sc-$n}
spec: {storageClassName: $sc, accessModes: [ReadWriteOnce], resources: {requests: {storage: $size}}}
---
apiVersion: v1
kind: Pod
metadata: {name: fio-$sc-$n, labels: {app: lvmo-fio}}
spec:
  $placement
  restartPolicy: Never
  containers:
  - name: fio
    image: alpine:3.20
    # All pods of a class prefill together, then measure together.
    command: [sh, -c, 'apk add --no-cache fio >/dev/null && while [ \$(date +%s) -lt $start ]; do sleep 1; done && fio /jobs/prefill.fio >&2 && while [ \$(date +%s) -lt $(( start + window )) ]; do sleep 1; done && fio --output-format=json /jobs/jobs.fio']
    volumeMounts: [{name: data, mountPath: /data}, {name: jobs, mountPath: /jobs}]
  volumes:
  - {name: data, persistentVolumeClaim: {claimName: perf-$sc-$n}}
  - {name: jobs, configMap: {name: fio-jobs}}
YAML
 done
 echo "running fio on $sc: $replicas pod(s) on $where, prefill in ${delay}s, measure $window s later" >&2
 deadline=$(( start + window + 5 * (runtime + 10) + 1800 ))
 while :; do
  running=$(k -n "$ns" get pods -l app=lvmo-fio -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' | grep -cvE '^(Succeeded|Failed)$' || true)
  (( running == 0 )) && break
  if (( $(date +%s) >= deadline )); then
   # Keep every pod's output before the namespace goes: it is the evidence.
   echo "timeout on $sc" >&2; k -n "$ns" get pods -o wide >&2
   mkdir -p "$out/$sc"
   for n in $(seq 1 "$replicas"); do k -n "$ns" logs "fio-$sc-$n" > "$out/$sc/$n.timeout.log" 2>&1 || true; done
   exit 1
  fi
  sleep 10
 done
 (( replicas > 1 )) && mkdir -p "$out/$sc"
 for n in $(seq 1 "$replicas"); do
  phase=$(k -n "$ns" get pod "fio-$sc-$n" -o jsonpath='{.status.phase}')
  file=$out/$sc.json; (( replicas > 1 )) && file=$out/$sc/$n.json
  if [[ $phase != Succeeded ]]; then
   # Keep the evidence and go on: under overload a backend may fail some pods.
   k -n "$ns" logs "fio-$sc-$n" > "${file%.json}.failed.log" 2>&1 || true
   k -n "$ns" describe pod "fio-$sc-$n" >> "${file%.json}.failed.log" 2>&1 || true
   echo "fio failed on $sc pod $n: $phase (see ${file%.json}.failed.log)" >&2
   (( replicas == 1 )) && exit 1
   continue
  fi
  k -n "$ns" logs "fio-$sc-$n" | sed -n '/^{/,$p' > "$file"
 done
 k -n "$ns" delete pods -l app=lvmo-fio --wait=true >/dev/null
 k -n "$ns" delete pvc --all --wait=true >/dev/null
done
if (( replicas == 1 )); then
 echo "| StorageClass | Seq write 1M (MiB/s) | Seq read 1M (MiB/s) | Rand write 4k (IOPS) | Rand read 4k (IOPS) | Rand read 4k QD1 p50 / p99 (µs) |"
 echo "|---|---|---|---|---|---|"
 for sc in "$@"; do
  jq -r --arg sc "$sc" '
   def job(n): .jobs[] | select(.jobname == n);
   def mib(x): (x / 1024 | . * 10 | round / 10);
   def us(x): (x / 1000 | round);
   "| \($sc) | \(mib(job("seq-write-1m").write.bw)) | \(mib(job("seq-read-1m").read.bw)) | \(job("rand-write-4k").write.iops | round) | \(job("rand-read-4k").read.iops | round) | \(us(job("rand-read-4k-qd1").read.clat_ns.percentile["50.000000"])) / \(us(job("rand-read-4k-qd1").read.clat_ns.percentile["99.000000"])) |"
  ' "$out/$sc.json"
 done
else
 echo "| StorageClass | Pods OK | Seq write 1M, total (MiB/s) | Seq read 1M, total (MiB/s) | Rand write 4k, total (IOPS) | Rand read 4k, total (IOPS) | Rand read 4k QD1: median pod p50 / worst pod p99 (µs) | Start spread (s) |"
 echo "|---|---|---|---|---|---|---|---|"
 for sc in "$@"; do
  jq -rs --arg sc "$sc" --argjson replicas "$replicas" '
   def job(n): [.[] | .jobs[] | select(.jobname == n)];
   def mib(x): (x / 1024 | round);
   def us(x): (x / 1000 | round);
   (job("rand-read-4k-qd1") | map(.read.clat_ns.percentile["50.000000"]) | sort) as $p50
   | "| \($sc) | \(length)/\($replicas) | \(mib(job("seq-write-1m") | map(.write.bw) | add)) | \(mib(job("seq-read-1m") | map(.read.bw) | add)) | \(job("rand-write-4k") | map(.write.iops) | add | round) | \(job("rand-read-4k") | map(.read.iops) | add | round) | \(us($p50[($p50 | length / 2 | floor)])) / \(us(job("rand-read-4k-qd1") | map(.read.clat_ns.percentile["99.000000"]) | max)) | \(map(.jobs[0].job_start) | (max - min) / 1000 | round) |"
  ' "$out/$sc"/*.json
 done
fi
