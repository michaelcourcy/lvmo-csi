---
type: eks
can-provide: [kubernetes, storage-server, test-storage-server, storage-server-reboot, nfs-client, iscsi-client, multi-node, distinct-initiators, node-power-control, kasten, object-storage, aws-storage]
creation: agent              # the agent may create it; only the contributor deletes the cluster
---

# eks: small EKS cluster on AWS with an Ubuntu storage server

## Shape

An EKS cluster with 3 Ubuntu 24.04 workers (t3.medium, managed node group), and one Ubuntu 24.04 EC2 instance in the same VPC running the lvmo-csi API as a test storage server (`scripts/setup-vm.sh`: VGs `lvmo-test1` and `lvmo-test2` on loop disks, direct CSI endpoint). Each worker is a separate VM with its own kernel and iSCSI initiator name. It is a cheaper alternative to `ocp-azure` for multi-node and iSCSI resilience scenarios: about $0.30 per hour while it exists (control plane, 4 × t3.medium, disks).

Nobody needs SSH. The storage server is reached through AWS Systems Manager (Session Manager for a shell, Run Command for scripts). Workers are reached with `kubectl debug node/<name>`.

## Cannot provide

- `kubevirt`: t3 instances have no nested virtualization. It would need metal instances.
- `cluster-on-storage-server`: the storage server has no `kubectl`, so `backend-routing` does not apply.
- `openshift`, `windows-guest-image`.

## Preflight

With `export AWS_REGION=eu-west-3 KUBECONFIG=~/.kube/lvmo-test-eks`:

- `kubectl get nodes` shows 3 `Ready` nodes.
- `kubectl -n lvmo-system get pods` shows the controller and 3 node pods ready.
- `aws ssm describe-instance-information --filters Key=tag:Name,Values=lvmo-test-storage` shows the storage server `Online`.
- On the storage server (Run Command or Session Manager): `systemctl is-active lvmo-loop lvmo-api lvmo-driver nfs-server` prints `active` four times, and `lvs` shows `lvmo-pool` in both VGs.
- On each worker, `kubectl debug node/<name> --profile=sysadmin --image=busybox -- chroot /host sh -c 'cat /etc/iscsi/initiatorname.iscsi; lsmod | grep ^iscsi_tcp'` shows an initiator name different from the other workers', and the `iscsi_tcp` module. Delete the `node-debugger-*` pods afterwards.

## Deploy a code change

- **Driver**: push the image to the environment's private ECR repository (Create step 5); the workers can pull from ECR without a pull secret:
  ```sh
  registry=$(aws sts get-caller-identity --query Account --output text).dkr.ecr.$AWS_REGION.amazonaws.com
  aws ecr get-login-password | docker login --username AWS --password-stdin $registry
  docker buildx build --platform linux/amd64 --build-arg VERSION=$tag -t $registry/lvmo-csi:$tag --push .
  helm upgrade lvmo charts/lvmo-csi -n lvmo-system --reset-then-reuse-values --set image.repository=$registry/lvmo-csi --set-string image.tag=$tag
  ```
  Never push development images to `michaelcourcy/lvmo-csi`.
- **API**: build `lvmo-csi` for `linux/amd64`, upload it with the same S3 transfer as in Create step 3, install it as `/usr/local/bin/lvmo-csi` with Run Command, and `systemctl restart lvmo-api`.

## Power-cycling a worker (`node-power-control`)

The node group's Auto Scaling group replaces a stopped instance unless told not to. Before stopping a worker:

```sh
asg=$(aws eks describe-nodegroup --cluster-name lvmo-test --nodegroup-name workers --query 'nodegroup.resources.autoScalingGroups[0].name' --output text)
aws autoscaling suspend-processes --auto-scaling-group-name "$asg" --scaling-processes HealthCheck ReplaceUnhealthy AZRebalance Launch Terminate
instance=$(kubectl get node <node> -o jsonpath='{.spec.providerID}' | awk -F/ '{print $NF}')
aws ec2 stop-instances --instance-ids "$instance"     # later: aws ec2 start-instances
```

Afterwards, resume them with `aws autoscaling resume-processes --auto-scaling-group-name "$asg"`.

Use `aws ec2 stop-instances --force` to simulate a power cut: a normal stop shuts the OS down cleanly and logs out its iSCSI sessions, which would not test fencing.

**A restarted worker loses its public IP.** AWS gives an automatically assigned public IP back to a restarted instance only if it has a single network interface, and the VPC CNI attaches more. Without a NAT gateway the worker then cannot reach the EKS API and stays `NotReady`. Before starting it again, give its primary interface an Elastic IP (tag it `project=lvmo-csi` and release it in Delete):

```sh
alloc=$(aws ec2 allocate-address --domain vpc --tag-specifications 'ResourceType=elastic-ip,Tags=[{Key=project,Value=lvmo-csi}]' --query AllocationId --output text)
eni=$(aws ec2 describe-instances --instance-ids "$instance" --query 'Reservations[0].Instances[0].NetworkInterfaces[?Attachment.DeviceIndex==`0`].NetworkInterfaceId' --output text)
aws ec2 associate-address --allocation-id "$alloc" --network-interface-id "$eni"
```

## Create

All commands run on the laptop with the AWS CLI, `eksctl`, `kubectl`, `helm`, `jq` and Go, from the repository root.

### 1. The cluster

```sh
export AWS_REGION=eu-west-3 KUBECONFIG=~/.kube/lvmo-test-eks
cat > /tmp/eks-lvmo-test.yaml <<'YAML'
apiVersion: eksctl.io/v1alpha5
kind: ClusterConfig
metadata:
  name: lvmo-test
  region: eu-west-3
  version: "1.35"
  tags: {project: lvmo-csi, purpose: automated-test}
vpc:
  nat: {gateway: Disable}   # nodes use public subnets; no NAT gateway cost
addons:
- name: vpc-cni
- name: coredns
- name: kube-proxy
- name: snapshot-controller
- name: aws-ebs-csi-driver   # for comparisons with AWS storage (aws-storage)
- name: aws-efs-csi-driver
managedNodeGroups:
- name: workers
  amiFamily: Ubuntu2404
  instanceType: t3.medium
  minSize: 3
  maxSize: 3
  desiredCapacity: 3
  volumeSize: 30
  privateNetworking: false
  labels: {lvmo-test: worker}
  tags: {project: lvmo-csi, purpose: automated-test}
  iam:
    withAddonPolicies: {ebs: true, efs: true}
  preBootstrapCommands:
  - apt-get update -qq
  - DEBIAN_FRONTEND=noninteractive apt-get install -y -qq open-iscsi nfs-common
  - systemctl enable --now iscsid
  - echo iscsi_tcp > /etc/modules-load.d/iscsi.conf
  - modprobe iscsi_tcp
YAML
eksctl create cluster -f /tmp/eks-lvmo-test.yaml --kubeconfig "$KUBECONFIG"
```

About 20 minutes. Kubernetes 1.35 matches the `e2e.test` version used by the Kind scenarios. The `snapshot-controller` EKS add-on installs the snapshot CRDs and controller. eksctl also adds the `metrics-server` add-on by default. The workers install the iSCSI initiator and NFS client at boot. `iscsid` must run on the host, because the node plugin uses host networking and the host's iSCSI daemon. The `iscsi_tcp` kernel module must be loaded on the host too: Ubuntu's AWS kernel ships it but does not load it, and `iscsiadm` inside the node plugin cannot load host modules (it fails with `could not find module by name='iscsi_tcp'`). A node created from a node group without these commands needs them run once, for example through `kubectl debug node/<name> --profile=sysadmin --image=busybox -- chroot /host sh -c 'echo iscsi_tcp > /etc/modules-load.d/iscsi.conf; modprobe iscsi_tcp'`.

### 2. The storage server's identity and network

```sh
aws iam create-role --role-name lvmo-test-storage --tags Key=project,Value=lvmo-csi \
 --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam attach-role-policy --role-name lvmo-test-storage --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore
aws iam create-instance-profile --instance-profile-name lvmo-test-storage
aws iam add-role-to-instance-profile --instance-profile-name lvmo-test-storage --role-name lvmo-test-storage

vpc=$(aws eks describe-cluster --name lvmo-test --query cluster.resourcesVpcConfig.vpcId --output text)
subnet=$(aws ec2 describe-subnets --filters Name=vpc-id,Values=$vpc Name=map-public-ip-on-launch,Values=true --query 'Subnets[0].SubnetId' --output text)
cidr=$(aws ec2 describe-vpcs --vpc-ids $vpc --query 'Vpcs[0].CidrBlock' --output text)
sg=$(aws ec2 create-security-group --group-name lvmo-test-storage --description "lvmo-csi test storage server" --vpc-id $vpc \
 --tag-specifications 'ResourceType=security-group,Tags=[{Key=project,Value=lvmo-csi}]' --query GroupId --output text)
for port in 50051 2049 3260; do aws ec2 authorize-security-group-ingress --group-id $sg --protocol tcp --port $port --cidr $cidr; done
```

The security group admits only the API, NFS and iSCSI ports, and only from inside the VPC. There is no SSH rule: access goes through Systems Manager. The public subnet gives the server outbound internet access for packages without a NAT gateway.

### 3. The storage server

```sh
ami=$(aws ssm get-parameter --name /aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id --query Parameter.Value --output text)
id=$(aws ec2 run-instances --image-id $ami --instance-type t3.medium --subnet-id $subnet --security-group-ids $sg \
 --iam-instance-profile Name=lvmo-test-storage --metadata-options HttpTokens=required \
 --block-device-mappings 'DeviceName=/dev/sda1,Ebs={VolumeSize=40,VolumeType=gp3,DeleteOnTermination=true}' \
 --tag-specifications 'ResourceType=instance,Tags=[{Key=Name,Value=lvmo-test-storage},{Key=project,Value=lvmo-csi}]' \
 --query 'Instances[0].InstanceId' --output text)
aws ec2 wait instance-running --instance-ids $id
private=$(aws ec2 describe-instances --instance-ids $id --query 'Reservations[0].Instances[0].PrivateIpAddress' --output text)
```

Wait until `aws ssm describe-instance-information --filters Key=InstanceIds,Values=$id` reports `Online` (about a minute; Ubuntu images include the SSM agent).

Transfer the scripts, scenarios and test binaries through a private, temporary bucket, then run the standard test-server setup with the released binaries:

```sh
mkdir -p /tmp/lvmo-eks/bin
for suite in sanity integration; do CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o /tmp/lvmo-eks/bin/$suite.test ./tests/$suite; done
tar -czf /tmp/lvmo-eks/source.tgz scripts tests -C /tmp/lvmo-eks bin
bucket="lvmo-test-transfer-$(date +%s)"
aws s3api create-bucket --bucket $bucket --create-bucket-configuration LocationConstraint=$AWS_REGION
aws s3api put-public-access-block --bucket $bucket --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
aws s3 cp /tmp/lvmo-eks/source.tgz s3://$bucket/source.tgz
url=$(aws s3 presign s3://$bucket/source.tgz --expires-in 3600)
version=v0.1.0-alpha.2   # or another published release
params=$(jq -n --arg url "$url" --arg ip "$private" --arg v "$version" '{commands:[
 "set -e",
 "curl -fsSL \"" + $url + "\" -o /tmp/lvmo-src.tgz",
 "mkdir -p /opt/lvmo-src && tar -xzf /tmp/lvmo-src.tgz -C /opt/lvmo-src && rm /tmp/lvmo-src.tgz",
 "STORAGE_SERVER=" + $ip + " RELEASE_VERSION=" + $v + " bash /opt/lvmo-src/scripts/setup-vm.sh > /var/log/lvmo-setup.log 2>&1 || { tail -40 /var/log/lvmo-setup.log; exit 1; }",
 "systemctl is-active lvmo-loop lvmo-api lvmo-driver nfs-server iscsid", "lvs"], executionTimeout:["1800"]}')
cmd=$(aws ssm send-command --instance-ids $id --document-name AWS-RunShellScript --parameters "$params" --query Command.CommandId --output text)
aws ssm wait command-executed --command-id $cmd --instance-id $id
aws ssm get-command-invocation --command-id $cmd --instance-id $id --query StandardOutputContent --output text
aws s3 rb s3://$bucket --force
```

The sources live in `/opt/lvmo-src` (not `/tmp`, which is emptied at reboot), so server-side scenarios run with `bash /opt/lvmo-src/scripts/run-scenarios.sh <id>`. `setup-vm.sh` also installs `lvmo-loop.service`, so the test VGs come back after a reboot.

### 4. The driver and test classes

```sh
context=$(kubectl config current-context)
kubectl create namespace lvmo-system
helm upgrade --install lvmo charts/lvmo-csi -n lvmo-system \
 --set image.repository=michaelcourcy/lvmo-csi --set-string image.tag=$version --wait --timeout 5m
KUBE_CONTEXT=$context API_ENDPOINT=$private:50051 bash scripts/install-storageclasses.sh
```

### Optional: performance storage

Only for `performance` scenarios, which compare lvmo with EBS and EFS. lvmo then needs a disk equivalent to an EBS volume, not a loop file:

- In step 3, use a non-burstable instance (`--instance-type m6i.large`), place it in the same availability zone as the benchmark worker (EBS volumes are zonal), and add a dedicated data disk with the same performance as the EBS class: `'DeviceName=/dev/sdf,Ebs={VolumeSize=100,VolumeType=gp3,Iops=3000,Throughput=125,DeleteOnTermination=true}'`.
- After `setup-vm.sh`, on the server: find the disk by its volume ID (`lsblk -dn -o NAME,SERIAL`, serial `vol…` without the dash), allow it in the LVM filter next to the loop devices, and create the VG:
  ```sh
  printf 'devices { global_filter = [ "a|^/dev/loop[0-9]+$|", "a|^%s$|", "r|.*|" ] }\n' "$dev" > /etc/lvm/lvmlocal.conf
  pvcreate -y "$dev" && vgcreate lvmo-perf "$dev"
  lvcreate --yes --type thin-pool -L 90G --poolmetadatasize 1G -n lvmo-pool lvmo-perf
  sed -i 's/lvmo-test1 lvmo-test2$/lvmo-test1 lvmo-test2 lvmo-perf/' /etc/systemd/system/lvmo-api.service
  systemctl daemon-reload && systemctl restart lvmo-api
  ```
- Create an EFS file system (General Purpose, Elastic throughput, encrypted) with a mount target in each of the cluster's subnets, behind a security group allowing TCP 2049 from the VPC.
- StorageClasses: `lvmo-perf-iscsi` and `lvmo-perf-nfs` (`vg: lvmo-perf`); `ebs-gp3` (`ebs.csi.aws.com`, `type: gp3`, `iops: "3000"`, `throughput: "125"`, `WaitForFirstConsumer`); `efs` (`efs.csi.aws.com`, `provisioningMode: efs-ap`, `fileSystemId`).
- VolumeSnapshotClass `ebs-snapshots` (`ebs.csi.aws.com`, `deletionPolicy: Delete`), to compare restored-volume reads with `lvmo-snapshots`.

### 5. The development image repository

```sh
aws ecr create-repository --repository-name lvmo-csi --image-tag-mutability MUTABLE --tags Key=project,Value=lvmo-csi
```

A private repository in the environment's account and region, used only for development images (see Deploy a code change).

### 6. The instance file

Write `.test/environments/<name>.md` (see the template) with `type: eks`, the kubeconfig path, the context, the storage server's instance id and private IP, the node group's Auto Scaling group name, and `created-by: agent`.

## Delete

Only what the run installed and the storage server. **Never delete the cluster**: the contributor does that, with `eksctl delete cluster --name lvmo-test --region eu-west-3`, when the environment is no longer needed.

```sh
kubectl delete -f tests/storageclasses.yaml --ignore-not-found
helm uninstall lvmo -n lvmo-system
kubectl delete namespace lvmo-system --ignore-not-found
aws ec2 terminate-instances --instance-ids $id && aws ec2 wait instance-terminated --instance-ids $id
aws ec2 delete-security-group --group-id $sg
aws iam remove-role-from-instance-profile --instance-profile-name lvmo-test-storage --role-name lvmo-test-storage
aws iam delete-instance-profile --instance-profile-name lvmo-test-storage
aws iam detach-role-policy --role-name lvmo-test-storage --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore
aws iam delete-role --role-name lvmo-test-storage
aws ecr delete-repository --repository-name lvmo-csi --force
```

With the optional performance storage, also delete the EFS file system's mount targets (`aws efs describe-mount-targets`, `aws efs delete-mount-target`), then the file system (`aws efs delete-file-system`) and its security group. The data disk is deleted with the storage server.

Release any Elastic IP tagged `project=lvmo-csi` that was added for a power-cycled worker. While its worker exists, releasing it makes the worker lose internet access again.

Delete the security group before deleting the cluster, or `eksctl delete cluster` cannot remove the VPC. Remove the instance file `.test/environments/<name>.md`, or, if the cluster is kept, update it to say the driver and storage server are gone.
