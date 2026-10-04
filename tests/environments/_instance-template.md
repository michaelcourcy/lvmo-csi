---
# Copy to .test/environments/<name>.md (gitignored). Never put secrets here:
# refer to kube contexts, SSH host aliases, cloud CLI profiles or secret names.
name: my-env
type: lima                   # a file in tests/environments/
provides: [kubernetes, storage-server, storage-server-reboot, nfs-client]
created-by: contributor      # contributor | agent
delete-after-run: false      # only honoured when created-by is agent
driver-install: agent-managed   # agent-managed: the agent may install, upgrade and uninstall lvmo
                                # preinstalled: use what is installed, never change it
image-registry: none         # extra registry for development images, if the type's own is not used; never michaelcourcy/lvmo-csi
---

# my-env

## Access

- Kubernetes context: `kind-lvmo-demo`, kubeconfig `~/lvmo-demo/kubeconfig`
- Storage server: how to get a root shell, e.g. `limactl shell lvmo-storage sudo -i` or `ssh lvmo-storage`
- API endpoint as seen from the nodes: `host.docker.internal:50051`
- VGs available to the API: `lvmo-data`

## Capability details

Anything a scenario needs to know beyond the capability names: which nodes may be powered off and how, where the Windows image is, which SSH key the guest accepts.

## Notes

Known quirks, shared users, time windows when the environment must not be disturbed.
