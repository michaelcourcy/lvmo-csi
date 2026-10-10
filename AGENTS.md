# Agent and contributor guide

This file is read by coding agents (Codex reads `AGENTS.md`; Claude Code reads it through `CLAUDE.md`) and by human contributors. It defines how tests are described, where they run, and what an agent may and may not do while running them.

For what the project is and how it is built, read [README.md](README.md). The original requirements are in [initial-prompt.md](initial-prompt.md); treat them as the reference when a test expectation seems to contradict the design.

## Every contribution proposes its tests

A change that adds or alters behaviour must add or update at least one scenario in [tests/scenarios/](tests/scenarios/). The scenario may be `proposed` (written before the code exists) or `manual` (implemented, not yet scripted). Automation can come later. A pull request without a scenario must explain why none applies, for example a documentation-only change.

## Test architecture

Three things are kept apart, because they have different lifetimes and owners:

| What | Where | Committed | Owner |
|---|---|---|---|
| **Scenario**: what to test and what must be true | `tests/scenarios/<id>.md` | Yes | The project |
| **Environment type**: a kind of platform, what it can provide, how to create and destroy it | `tests/environments/<type>.md` | Yes | The project |
| **Environment instance**: one contributor's actual platform (contexts, hosts, policies) | `.test/environments/<name>.md` | No (`.test/` is gitignored) | The contributor |
| **Run request**: which scenarios to run on which instances | The prompt given to the agent | No | The person running the session |
| **Report**: what happened, with evidence | `.test/reports/<date>-<instance>-<scenario>.md` | No | The agent |

Scenarios survive; environments are provided by each contributor. A run request looks like:

> Execute `windows-live-migration` on `ocpa` and on `harvester-lan`.

or, by group:

> Pass the `basic` tests on `harvester-lan`.

### Capabilities

Scenarios declare the capabilities they **require**; environment instances declare the capabilities they **provide**. A scenario can only run on an instance that provides all of its requirements.

| Capability | Meaning |
|---|---|
| `kubernetes` | A cluster where the driver can be installed with Helm, with cluster-admin rights for the session |
| `openshift` | The cluster is OpenShift (SCCs, `oc`, OpenShift Virtualization available as an operator) |
| `storage-server` | A Linux host running the lvmo-csi API with at least one VG holding the `lvmo-pool` thin pool, reachable from every node |
| `test-storage-server` | The storage server was set up by `scripts/setup-vm.sh` for testing only: VGs `lvmo-test1` and `lvmo-test2`, `lvmo-driver` serving CSI directly on `unix:///tmp/lvmo-csi.sock`, a root shell for the agent, and nothing else stored on it |
| `cluster-on-storage-server` | The cluster can be driven with `kubectl` and `helm` from the storage server itself, as in `lima-nested` |
| `nfs-client` | Every node can mount NFS exports from the storage server |
| `iscsi-client` | Every node has a working iSCSI initiator that can log in to the storage server's targets. Kind on Docker Desktop does **not** provide this |
| `storage-server-reboot` | The agent may reboot the storage server |
| `multi-node` | At least two schedulable worker nodes |
| `distinct-initiators` | Each worker has its own kernel and its own iSCSI daemon, so that its sessions stop with it. lvmo gives each node its own initiator name; Kind nodes on one host still do **not** provide this: they share one kernel and one `iscsid` |
| `node-power-control` | The agent may power a worker off and on, so that its kernel really stops. Stopping a Kind node container does not qualify |
| `kubevirt` | KubeVirt or OpenShift Virtualization is installed and live migration works (needs hardware virtualization) |
| `windows-guest-image` | A Windows VM image, with QEMU guest agent and OpenSSH server enabled, is available to the cluster. Agents never download or license one |
| `aws-storage` | The cluster runs on AWS with the EBS and EFS CSI drivers, so that lvmo can be compared with AWS storage |
| `kasten` | Kasten K10 is installed, or the agent may install it |
| `object-storage` | An S3-compatible bucket the run may write to, with credentials limited to it, or the agent may create one |

Add a capability here before using it in a scenario or environment type.

### Groups

A scenario belongs to zero or more groups, so that a run request can name a set of scenarios at once. A group is a convenience, not a requirement: each scenario in it is still checked against the environment's capabilities, and the ones that do not fit are reported as not applicable.

| Group | Scenarios that check |
|---|---|
| `basic` | The driver's basic contract: CSI conformance, the upstream Kubernetes storage suite, ordinary snapshot restore |
| `backup` | Snapshot-based backup and restore, with or without changed block tracking |
| `cbt` | Changed block tracking (KEP-3314) |
| `routing` | One driver serving several storage servers |
| `resilience` | Recovery from failures: failed operations, reboots, node loss |
| `kubevirt` | Virtual machine workloads |
| `performance` | Measurements, compared with other storage; results go to [docs/performances-test.md](docs/performances-test.md) |

Add a group here before using it in a scenario. `cleanup-audit` belongs to no group: it runs last after any set of scenarios on a `test-storage-server`.

### Scenario format

Copy [tests/scenarios/_template.md](tests/scenarios/_template.md). The file name is the scenario id. Front matter:

- `id`: same as the file name without `.md`.
- `status`: `proposed` (the behaviour does not exist yet; running it means implementing it), `manual` (follow the steps), or `automated` (run the command in `automation`; the steps describe what it does).
- `groups`: list of groups, possibly empty.
- `requires`: list of capabilities.
- `automation`: `scripts/run-scenarios.sh <id>`, or `none`. Automating a scenario means adding its id to [scripts/run-scenarios.sh](scripts/run-scenarios.sh).

Sections: **Purpose**, **Preconditions**, **Steps** (numbered, concrete, each one checkable), **Expected** (pass criteria: observable, unambiguous), **Evidence** (what the report must contain), **Cleanup**, and optionally **Observations** (things to record that are not pass criteria) and **Design notes**.

Write steps that a person or an agent could follow without reading the code. Give names, sizes, timeouts and commands where they matter.

### Environment type format

Each file in [tests/environments/](tests/environments/) declares in its front matter `can-provide` (the most an instance of this type can offer) and `creation` (`agent` if an agent may create one, `contributor-only` otherwise). Then come these sections, in this order:

1. **Shape**: what the platform is.
2. **Cannot provide**: capabilities it lacks, and why. Optional.
3. **Preflight**: how to check an instance is healthy before a run.
4. **Deploy a code change**: how to get locally changed code onto it.
5. **Create**: how to build one.
6. **Delete**: how to tear one down.

Create and Delete are always the last two sections. They can be as detailed as a full walkthrough (see [lima.md](tests/environments/lima.md)) or as short as "Never create: use the existing environment" and "Never delete: only remove what the run installed". To support a new platform, such as an EKS cluster, add a type file.

### Environment instance format

Copy [tests/environments/_instance-template.md](tests/environments/_instance-template.md) to `.test/environments/<name>.md`. It names the type, lists the capabilities this instance really provides (which may be fewer than its type allows), how to reach it, and the lifecycle policy. **Never put secrets in it**: refer to kube contexts, SSH host aliases, cloud CLI profiles, or secret names, not to tokens or passwords.

## How an agent runs a test request

Follow these steps in order. Stop and ask whenever a step says so; do not guess.

### 1. Identify scenarios and environments

Take the scenario ids, groups and instance names from the prompt. Expand each group into its scenarios using their `groups` field. If no environment is named, ask which one. Do not default to creating one.

### 2. Resolve each environment instance

Read `.test/environments/<name>.md`.

- **It exists**: run its preflight (step 4).
- **It does not exist**: stop and ask the user whether you should create it.
  - If **no**, skip every run on that environment and say so.
  - If **yes**, check the type file. If its creation is `contributor-only` (a physical lab, a cluster you cannot provision), say so and ask the user to provide it instead. Otherwise ask whether it should be **deleted after the test** or **kept for reuse**. Then create it following the type's **Create** section and write the instance file, recording `created-by: agent` and the answer in `delete-after-run`.

Environments are expensive. Reuse them by default, and leave them as you found them.

### 3. Check consistency before touching anything

For every (scenario, environment) pair:

- **Capabilities**: every `requires` entry must be in the instance's `provides`. If not, mark the pair *not applicable*, name the missing capabilities, and do not run it. Example: `windows-live-migration` on a `lima` instance is not applicable: Lima + Kind provides neither `kubevirt` nor `distinct-initiators`.
- **The scenario itself**: its Expected section must not contradict itself, its own steps, or the design in [initial-prompt.md](initial-prompt.md) and [README.md](README.md). Example: expecting a `ReadWriteMany` *Filesystem* iSCSI volume to be accepted contradicts the rule that only Block mode may be shared.

Report every inconsistency to the user and wait for an answer before running the affected pairs. Run the consistent pairs only once the user has confirmed.

### 4. Preflight the environment

Check that it is reachable, that the storage server API is healthy, and which lvmo version is installed. The instance's `driver-install` policy says whether you may install or upgrade the driver. Never overwrite an installation the policy does not let you manage.

### 5. Execute

Follow the steps exactly and record the evidence the scenario asks for, as you go. For an `automated` scenario, run its `automation` command where its Steps say (on the storage server, or wherever the cluster's kube context works). When several scenarios run on a `test-storage-server`, run `cleanup-audit` last. Never mark a step passed without evidence. Do not skip, reorder, or loosen steps.

### 6. When a step fails

Find the cause before changing anything:

- **The product is wrong or the feature is missing** (always the case for `proposed` scenarios): make the code changes needed for the scenario to pass. Follow the conventions of the surrounding code, add unit tests where the logic allows it, rebuild, redeploy to the environment, and rerun the scenario from the start. Rerun other scenarios on the same environment that the change could affect.
- **The environment is broken** (unreachable node, full disk, expired credentials): fix it only if that is within what the instance file allows; otherwise report it.
- **The expectation is wrong or inconsistent** (it contradicts the design, another scenario, or physical reality): stop. Do not change the code to satisfy it, and do not edit Expected to make the run pass. Explain the inconsistency and ask.

Changing a scenario's Expected section always needs the user's approval.

### 7. Clean up

Run the scenario's Cleanup after every run, passed or failed. Then:

- If the instance has `created-by: agent` and `delete-after-run: true`, delete the environment following the type's **Delete** section and remove the instance file.
- Otherwise leave the environment in place. Never delete an environment the agent did not create in this session.

### 8. Report

Write `.test/reports/<YYYY-MM-DD>-<instance>-<scenario>.md` with: lvmo commit and version, environment instance and its capabilities, result per step (passed, failed, not run) with evidence, code changes made, cleanup outcome, and observations. Then summarise in the conversation: one line per pair. If the run validates something not yet listed in [docs/validation.md](docs/validation.md), propose an update; do not edit it without asking.

### Commits and publishing

Commit, push, or create releases only when the person running the session asks.

When Codex creates a commit for work it contributed to, include the trailer `Co-Authored-By: Codex <noreply@openai.com>`. This applies to future commits only; do not rewrite existing history to add attribution. Preserve any other co-author trailers.

**Images.** `michaelcourcy/lvmo-csi` and `michaelcourcy/lvmo-csi-storage-server` on Docker Hub are release repositories: only the release workflow publishes to them, when a `v*` tag is pushed. Never push a development image there. A development image goes to the environment's own registry, as its type file describes under **Deploy a code change** (loaded into Kind, OpenShift's internal registry, a private ECR repository created with the environment…), or to the registry named in the instance file's `image-registry`. If neither applies, ask.

## Running automated scenarios without an agent

[scripts/run-scenarios.sh](scripts/run-scenarios.sh) runs automated scenarios by id or group, then `cleanup-audit`: `scripts/run-scenarios.sh basic`. Scenarios that are `manual` or `proposed` are reported and skipped. `--resolve` only prints the selected ids. The original suite names (`sanity`, `external`, `snapshots`, `metadata`) still work as aliases.

[scripts/e2e.sh](scripts/e2e.sh) takes the same arguments and runs them in a disposable `lima-nested` environment, or on Azure with `azure`. `KEEP_TEST_ENV=true` keeps the Lima VM afterwards. Unit tests run with `go test ./...` on macOS; packages that need a Linux CSI endpoint skip themselves.
