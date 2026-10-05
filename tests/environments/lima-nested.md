---
type: lima-nested
can-provide: [kubernetes, storage-server, test-storage-server, cluster-on-storage-server, nfs-client, iscsi-client]
creation: agent              # agent | contributor-only
---

# lima-nested: Kind inside a Lima VM that is also the storage server

## Shape

One Lima VM (Docker-rootful template) runs the lvmo-csi API, NFS server and iSCSI target on two loop-backed VGs (`lvmo-test1`, `lvmo-test2`), and a Kind cluster inside the same VM. This is the layout built by [scripts/e2e.sh](../../scripts/e2e.sh) and the one the recorded validation results used. Kind nodes reach the host's iSCSI initiator through the chart's `iscsiHostProc` helper.

## Cannot provide

- `storage-server-reboot`: rebooting the storage server also reboots the cluster, so the scenario no longer tests what it should.
- `multi-node`, `distinct-initiators`, `node-power-control`: all Kind nodes share the VM's kernel and `iscsid`, although lvmo gives each its own initiator name.
- `kubevirt`, `openshift`.

## Preflight

- `limactl list` shows the VM `Running`.
- In the VM: `systemctl is-active lvmo-api` is `active`, and `lvs` shows `lvmo-pool` in both VGs.
- `limactl shell <vm> sudo kubectl --context kind-lvmo-e2e -n lvmo-system get pods` shows the controller and node pods ready.

## Deploy a code change

Rerun the setup from a fresh source archive, as `scripts/e2e.sh` does: build with `make build` and the test binaries, copy the source archive into the VM, and run `scripts/setup-vm.sh` then `scripts/setup-kind.sh` inside it. Then run scenarios inside the VM with `sudo bash /tmp/lvmo-src/scripts/run-scenarios.sh <id|group>`.

## Create

```sh
KEEP_TEST_ENV=true scripts/e2e.sh local csi-sanity
```

This creates the VM `lvmo-test-<timestamp>`, installs everything, runs the given scenario and the cleanup audit, and keeps the VM. Pick the shortest scenario you need. Then write the instance file `.test/environments/<name>.md` with `type: lima-nested` and the VM name.

## Delete

```sh
limactl delete --force <vm>
```

Remove the instance file `.test/environments/<name>.md`.
