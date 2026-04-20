# DBaaS Controller — Architecture & Design

A Kubernetes-native Database-as-a-Service that provisions fully isolated, managed PostgreSQL instances on [Harvester HCI](https://harvesterhci.io/) v1.7.1 using KubeVirt VMs, CDI storage, and Kube-OVN network isolation.

## Overview

The DBaaS controller follows the **async CRD + reconciler** pattern: a user (or the REST gateway) creates a `DBInstance` custom resource, and the controller walks through a phase-based state machine to provision all underlying Harvester resources. Each phase is idempotent and crash-safe — the controller reads `status.provisioningPhase` and `status.resources` to determine what's done and resumes from the next step.

```
User / REST API
       │
       │  kubectl apply / HTTP POST
       ▼
┌────────────────────────────────────────────────────────────┐
│  Kubernetes API Server (etcd)                              │
│  DBInstance CRD — spec (desired) + status (observed)       │
└──────────────┬─────────────────────────────────────────────┘
               │  watch + reconcile
               ▼
┌────────────────────────────────────────────────────────────┐
│  DBInstance Controller (controller-runtime)                 │
│  One phase per reconcile cycle, requeue on advance          │
└──────────────┬─────────────────────────────────────────────┘
               │  Dynamic client calls
               ▼
┌────────────────────────────────────────────────────────────┐
│  Harvester 1.7.1                                           │
│  Kube-OVN │ CDI │ KubeVirt │ Prometheus/Grafana            │
└────────────────────────────────────────────────────────────┘
```

## Custom Resource Definitions

All CRDs are **cluster-scoped** under API group `dbaas.wso2.com/v1alpha1`.

### DBInstance (shortName: `dbi`)

The primary resource. Each DBInstance maps to one PostgreSQL VM with its own VPC, storage, monitoring, and credentials.

**Key spec fields:**

| Field | Default | Description |
|-------|---------|-------------|
| `dbInstanceClass` | (required) | Maps to CPU/RAM/connections — e.g. `db.m5.large` |
| `engineVersion` | `"16"` | PostgreSQL major version |
| `dbName` | instance name | Initial database to create |
| `port` | `5432` | PostgreSQL listen port |
| `masterUsername` | `"dbadmin"` | Admin user created by cloud-init |
| `allocatedStorage` | (required) | Data volume size in GiB |
| `storageType` | `"longhorn"` | Longhorn StorageClass |
| `dbSubnetGroupName` | `"10.50.0.0/24"` | Consumer VLAN CIDR allowed to reach the DB |
| `osImage` | `"ubuntu-22.04-server-cloudimg-amd64.img"` | Harvester VirtualMachineImage name or display name |
| `vmPassword` | (empty) | VM console password for dev/debug — omit in production |
| `vpcPeering` | (optional) | `{remoteVpc, remoteSubnet}` for Kube-OVN VPC peering |
| `deletionProtection` | `false` | Prevents accidental `kubectl delete` |
| `running` | `true` | `false` stops the VM (preserves storage) |
| `backupRetentionPeriod` | `0` | Days to retain pgBackRest backups (0 = disabled) |
| `s3BackupConfig` | (optional) | S3 endpoint, bucket, region, secretRef |
| `tags` | (optional) | User-defined labels |

**Key status fields:**

| Field | Description |
|-------|-------------|
| `phase` | RDS-style: `creating`, `available`, `stopping`, `stopped`, `starting`, `modifying`, `deleting`, `failed` |
| `provisioningPhase` | Internal phase: `Pending` → `NamespaceCreated` → ... → `Available` |
| `endpoint` | `{address, port, jdbcUrl}` — populated at `DatabaseReady` |
| `masterUserSecret` | `{name, status}` — K8s Secret with credentials |
| `resources` | Tracks every created resource for idempotency and cleanup |
| `grafanaUrl` | Per-instance Grafana dashboard URL |
| `prometheusTarget` | `pg-{id}-metrics.{ns}.svc:9187` |
| `observedGeneration` | Spec version that has been fully reconciled |

### DBSnapshot (shortName: `dbs`)

Point-in-time backup metadata: `dbInstanceRef`, `snapshotType` (full/diff/incr); status tracks S3 path, size, completion time.

### DBParameterGroup (shortName: `dbpg`)

Reusable PostgreSQL configuration: `family`, `description`, `parameters` (key-value map applied to `postgresql.conf`).

## Provisioning Phases

Each reconcile advances exactly one phase, updates status, and requeues.

```
Pending
  │  Create namespace dbaas-{id}
  ▼
NamespaceCreated
  │  Create VPC → NAD → Subnet (Kube-OVN)
  ▼
NetworkProvisioned
  │  Create CDI DataVolume (blank block storage)
  ▼
StorageProvisioned
  │  Resolve VM image → Create credentials Secret → Create KubeVirt VM
  ▼
VMCreated
  │  Poll VMI: phase=Running, uptime > 3 min
  ▼
WaitingForCloudInit
  │  VM IP + PostgreSQL accepting connections
  ▼
DatabaseReady
  │  Create headless Service + ServiceMonitor (non-fatal)
  ▼
MonitoringDeployed
  │  Create VPC peering if spec.vpcPeering set (non-fatal)
  ▼
VpcPeeringCreated
  │  Set phase=available, record observedGeneration
  ▼
Available ✓
```

**Crash recovery:** On restart, the controller reads `status.provisioningPhase` and `status.resources` from the CRD. Each phase checks if its resource already exists (via status fields) and skips if done. All `Create` calls tolerate `AlreadyExists`.

## Network Architecture

Each database instance gets complete network isolation via a dedicated Kube-OVN VPC.

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│  Harvester 1.7.1 Cluster                                                        │
│                                                                                 │
│  ┌───────────────────────────────────────────────────────────┐                  │
│  │  Namespace: dbaas-system                                  │                  │
│  │                                                           │                  │
│  │  ┌─────────────────────────────────────────────────────┐  │                  │
│  │  │  dbaas-controller Pod                                │  │                  │
│  │  │  :8080 REST Gateway  ◄── kubectl / curl             │  │                  │
│  │  │  :8081 Metrics       ◄── Prometheus scrape           │  │                  │
│  │  │  :8082 Health probes ◄── kubelet liveness/readiness  │  │                  │
│  │  │  Watches: DBInstance CRDs (etcd)                     │  │                  │
│  │  │  Creates: VPC, Subnet, NAD, DV, VM, Secret, SM      │  │                  │
│  │  └─────────────────────────────────────────────────────┘  │                  │
│  └───────────────────────────────────────────────────────────┘                  │
│         │                                                                       │
│         │ Reconcile loop (one phase per iteration)                              │
│         ▼                                                                       │
│  ┌──────────────────────────────────────────────────────────────────────────┐   │
│  │  Kube-OVN VPC: dbaas-{id}-vpc                                            │   │
│  │  staticRoutes: [{cidr: consumerVLAN, nextHopIP: ...}]                    │   │
│  │                                                                          │   │
│  │  ┌────────────────────────────────────────────────────────────────────┐  │   │
│  │  │  Kube-OVN Subnet: dbaas-{id}-subnet                               │  │   │
│  │  │  CIDR: 10.A.B.0/24 (FNV-32a hash, 32K possible)                   │  │   │
│  │  │  Gateway: 10.A.B.1                                                 │  │   │
│  │  │  provider: dbaas-{id}-nad.dbaas-{id}.ovn                          │  │   │
│  │  │  private: true    enableDHCP: true                                 │  │   │
│  │  │  allowSubnets: [consumerVLAN]                                      │  │   │
│  │  │                                                                    │  │   │
│  │  │  Namespace: dbaas-{id}                                             │  │   │
│  │  │  ┌──────────────────────────────────────────────────────────────┐  │  │   │
│  │  │  │  NAD: dbaas-{id}-nad                                         │  │  │   │
│  │  │  │  Labels: type=OverlayNetwork, clusternetwork=mgmt            │  │  │   │
│  │  │  └──────────────────────────────────────────────────────────────┘  │  │   │
│  │  │                                                                    │  │   │
│  │  │  ┌──────────────────────────────────────────────────────────────┐  │  │   │
│  │  │  │  KubeVirt VM: pg-{id}                                        │  │  │   │
│  │  │  │                                                              │  │  │   │
│  │  │  │  NIC 1: mgmt-net (pod network, masquerade)                  │  │  │   │
│  │  │  │    IP: 10.0.2.2 (NAT'd to pod IP)                          │  │  │   │
│  │  │  │    Purpose: internet, apt, DNS, monitoring                  │  │  │   │
│  │  │  │                                                              │  │  │   │
│  │  │  │  NIC 2: vpc-net (Kube-OVN bridge via NAD)                   │  │  │   │
│  │  │  │    IP: 10.A.B.X (DHCP from Kube-OVN)                       │  │  │   │
│  │  │  │    Purpose: PostgreSQL application traffic (isolated)       │  │  │   │
│  │  │  │                                                              │  │  │   │
│  │  │  │  PostgreSQL 14                                               │  │  │   │
│  │  │  │    listen_addresses = '*', port = {spec.port}               │  │  │   │
│  │  │  │    pg_hba: host all all 0.0.0.0/0 scram-sha-256            │  │  │   │
│  │  │  │                                                              │  │  │   │
│  │  │  │  Disks:                                                      │  │  │   │
│  │  │  │    os-disk  ← pg-{id}-os (image clone, 20Gi)               │  │  │   │
│  │  │  │    pgdata   ← pg-{id}-data (Longhorn, allocatedStorage)     │  │  │   │
│  │  │  │    cloudinit ← Secret pg-{id}-credentials (userdata key)    │  │  │   │
│  │  │  └──────────────────────────────────────────────────────────────┘  │  │   │
│  │  │                                                                    │  │   │
│  │  │  ┌──────────────────────────────────────────────────────────────┐  │  │   │
│  │  │  │  Monitoring                                                  │  │  │   │
│  │  │  │  Service: pg-{id}-metrics (headless, :9187)                  │  │  │   │
│  │  │  │  ServiceMonitor: pg-{id}-monitor (15s scrape)                │  │  │   │
│  │  │  └──────────────────────────────────────────────────────────────┘  │  │   │
│  │  └────────────────────────────────────────────────────────────────────┘  │   │
│  └──────────────────────────────────────────────────────────────────────────┘   │
│                                                                                 │
│  ═══════════════════════  VPC Boundary (isolated)  ═══════════════════════════  │
│                                                                                 │
│  ┌──────────────────────────────────────────────────────────────────────────┐   │
│  │  Consumer VLAN (from spec.dbSubnetGroupName)                             │   │
│  │  Application pods / RKE2 workloads connect via:                         │   │
│  │    jdbc:postgresql://10.A.B.X:5432/{dbName}?ssl=true                     │   │
│  │  Traffic allowed via: subnet.spec.allowSubnets + VPC static route       │   │
│  └──────────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────────┘
```

### VPC Peering

For workloads in a separate Kube-OVN VPC to access the database:

```
Application VPC (e.g. 10.99.0.0/24)         DBaaS VPC (e.g. 10.227.122.0/24)
  microservice pod                              pg-{id} VM
  eth0: pod network (HTTP)                      enp1s0: pod network (internet)
  net1: app VPC (10.99.0.x)                     enp2s0: dbaas VPC (10.A.B.x)
       │                                              ▲
       │  route: 10.227.122.0/24                       │
       │  → nextHopIP 169.254.0.1                      │
       ▼                                              │
  ┌─────────────────────────────────────────────────┐
  │  Kube-OVN VPC Peering (link-local tunnel)       │
  │  169.254.0.2/30  ◄──────────► 169.254.0.1/30   │
  └─────────────────────────────────────────────────┘
```

Both VPCs declare peering via `spec.vpcPeerings` with matching link-local IPs. Static routes on each VPC direct cross-VPC traffic through the peering link. The DBaaS subnet's `allowSubnets` must include the application VPC's CIDR.

Multi-homed pods (dual NIC) need an explicit route: `ip route add <dbaas-cidr> via <app-vpc-gateway> dev net1` — otherwise traffic defaults to `eth0` (pod network) and bypasses the peering link.

### Data Flow Summary

| Path | Source → Destination | Network |
|------|---------------------|---------|
| App → DB | Consumer VLAN / peered VPC → VM vpc-net (10.A.B.X:5432) | Kube-OVN VPC, `allowSubnets` enforced |
| Cloud-init | VM mgmt-net (10.0.2.2) → internet | Pod network (masquerade → node NAT) |
| Monitoring | Prometheus → VM mgmt-net:9187 | Pod network via headless Service |
| API | User → controller :8080 → CRD → reconciler → Harvester APIs | Cluster internal |
| Console | Harvester UI → VM serial console | KubeVirt subresource |

### Subnet CIDR Derivation

Each instance gets a `/24` subnet. The CIDR is derived from an FNV-32a hash of the instance name:

```
A = 100 + ((hash >> 8) & 0x7F)   → range 100–227
B = hash & 0xFF                   → range 0–255
CIDR = 10.A.B.0/24
Gateway = 10.A.B.1
```

This yields 32,768 possible subnets with minimal collision risk.

## VM Specification

### Dual NIC Layout

| NIC | Name | KubeVirt Type | Network | Purpose |
|-----|------|--------------|---------|---------|
| enp1s0 | `mgmt-net` | Pod (masquerade) | Kubernetes pod network | Internet (apt), DNS, monitoring scrape |
| enp2s0 | `vpc-net` | Multus (bridge) | Kube-OVN VPC subnet | PostgreSQL client traffic (isolated) |

### Disk Layout

| Disk | Source | Size | Mode |
|------|--------|------|------|
| `os-disk` | Harvester VirtualMachineImage (clone) | 20 GiB | ReadWriteMany, Block |
| `pgdata-disk` | Blank CDI DataVolume (Longhorn) | `spec.allocatedStorage` GiB | ReadWriteOnce, Block |
| `cloudinit` | K8s Secret `pg-{id}-credentials` key `userdata` | — | cloudInitNoCloud |

The OS disk uses Harvester's image-managed StorageClass via the `harvesterhci.io/imageId` annotation. The controller resolves the image by name or display name using `resolveVMImage`.

### Cloud-Init Bootstrap

On first boot, cloud-init:

1. (Optional) Sets VM console password if `spec.vmPassword` is set
2. Installs `postgresql`, `postgresql-contrib`, `jq` via apt
3. Writes `/etc/netplan/60-vpc-net.yaml` to auto-configure enp2s0 via DHCP
4. Writes `/etc/dbaas/bootstrap.env` with credentials and configuration
5. Writes `/etc/dbaas/bootstrap.sh` which:
   - Sets `listen_addresses = '*'` and configured port in `postgresql.conf`
   - Sets `max_connections` from instance class
   - Appends `scram-sha-256` remote auth rules to `pg_hba.conf`
   - Restarts PostgreSQL
   - Creates the admin user (SUPERUSER) and application database if not exists
6. Runs `netplan apply` → `bootstrap.sh`

### Credentials Secret

Secret `pg-{id}-credentials` in namespace `dbaas-{id}` contains:

| Key | Source | Length |
|-----|--------|--------|
| `admin_user` | `spec.masterUsername` | — |
| `admin_password` | `crypto/rand` | 32 chars |
| `repl_password` | `crypto/rand` | 32 chars |
| `exporter_password` | `crypto/rand` | 24 chars |
| `luks_key` | `crypto/rand` | 64 chars |
| `userdata` | Generated cloud-init YAML | — |

## REST Gateway

The controller embeds a lightweight HTTP gateway for RDS-style API access.

| Method | Path | Description | Response |
|--------|------|-------------|----------|
| `GET` | `/healthz` | Health check | `200 {"status":"ok"}` |
| `GET` | `/dbinstances` | List all DBInstances | `200` DBInstanceList |
| `POST` | `/dbinstances` | Create DBInstance | `202` (async) |
| `GET` | `/dbinstances/{name}` | Get DBInstance status | `200` or `404` |
| `DELETE` | `/dbinstances/{name}` | Delete DBInstance | `202` (async) |
| `POST` | `/dbinstances/{name}/start` | Start stopped instance | `202` |
| `POST` | `/dbinstances/{name}/stop` | Stop running instance | `202` |

**Ports:** `:8080` (gateway), `:8081` (controller metrics), `:8082` (health probes)

The gateway has no built-in authentication. In production, place it behind an auth-enforcing ingress or API gateway (OIDC, mTLS, or API key).

## Operations

### Stop / Start

```bash
kubectl patch dbi orders-prod --type merge -p '{"spec":{"running":false}}'   # stop
kubectl patch dbi orders-prod --type merge -p '{"spec":{"running":true}}'    # start
```

Stopping sets `spec.running=false` on the KubeVirt VM, preserving all storage.

### Live Resize

```bash
kubectl patch dbi orders-prod --type merge -p '{"spec":{"dbInstanceClass":"db.m5.xlarge","allocatedStorage":200}}'
```

The controller detects `generation != observedGeneration` on an available instance, resizes the VM (CPU/memory) and DataVolume concurrently, then marks available.

### Deletion

```bash
kubectl patch dbi orders-prod --type merge -p '{"spec":{"deletionProtection":false}}'
kubectl delete dbi orders-prod
```

The finalizer `dbaas.wso2.com/cleanup` triggers `TeardownAll`: concurrent deletion of ServiceMonitor, VM, DataVolume, Secret, NAD, Subnet, VPC peering, VPC, and the namespace.

## Instance Classes

| Class | vCPU | RAM | Max Connections |
|-------|------|-----|----------------|
| `db.t3.micro` | 1 | 1 GB | 50 |
| `db.t3.small` | 1 | 2 GB | 100 |
| `db.t3.medium` | 2 | 4 GB | 150 |
| `db.t3.large` | 2 | 8 GB | 200 |
| `db.t3.xlarge` | 4 | 16 GB | 300 |
| `db.m5.large` | 2 | 8 GB | 200 |
| `db.m5.xlarge` | 4 | 16 GB | 400 |
| `db.m5.2xlarge` | 8 | 32 GB | 600 |
| `db.m5.4xlarge` | 16 | 64 GB | 1000 |
| `db.r5.large` | 2 | 16 GB | 300 |
| `db.r5.xlarge` | 4 | 32 GB | 500 |
| `db.r5.2xlarge` | 8 | 64 GB | 800 |

## Monitoring

Each database instance gets automatic Prometheus + Grafana integration:

- **Headless Service** `pg-{id}-metrics` on port `9187` (postgres_exporter)
- **ServiceMonitor** `pg-{id}-monitor` with 15-second scrape interval
- **Grafana dashboard** at `{grafanaURL}/d/dbaas-{id}/postgresql-{id}`
- **Prometheus target** at `pg-{id}-metrics.dbaas-{id}.svc:9187`

Monitoring failure is non-fatal — the database reaches `Available` even if the ServiceMonitor can't be created (e.g., Prometheus Operator not installed).

## RBAC

The controller runs as `ServiceAccount: dbaas-controller` in `dbaas-system` with a `ClusterRole` granting:

| API Group | Resources | Verbs |
|-----------|-----------|-------|
| `dbaas.wso2.com` | DBInstance, DBSnapshot, DBParameterGroup + status/finalizers | Full CRUD |
| Core | Namespaces, Secrets, Services, Events | Create/delete as needed |
| `kubeovn.io` | VPCs, Subnets, VPC-peerings | Create/update/delete |
| `k8s.cni.cncf.io` | NetworkAttachmentDefinitions | Create/delete |
| `cdi.kubevirt.io` | DataVolumes | Full CRUD |
| `kubevirt.io` | VirtualMachines, VirtualMachineInstances | CRUD / read |
| `harvesterhci.io` | VirtualMachineImages | Read-only |
| `monitoring.coreos.com` | ServiceMonitors | Create/delete |
| `coordination.k8s.io` | Leases (leader election) | Full CRUD |

## Security Model

1. **Callers → DBaaS API:** Place TLS + auth (OIDC/mTLS) in front of the REST gateway
2. **DBaaS controller → Harvester:** Uses scoped K8s ServiceAccount + RBAC
3. **DB credentials:** Auto-generated, stored in K8s Secrets, never logged
4. **Network isolation:** Kube-OVN VPC with explicit `allowSubnets`
5. **VM access:** Console password only if `vmPassword` set (dev only)

## Project Structure

```
dbaas/
├── api/v1alpha1/               # CRD Go types + deepcopy
│   ├── types.go                # DBInstance, DBSnapshot, DBParameterGroup
│   ├── groupversion_info.go
│   └── zz_generated.deepcopy.go
├── internal/
│   ├── controller/             # Reconciler (phase-based state machine)
│   │   └── dbinstance_reconciler.go
│   ├── gateway/                # REST API gateway
│   │   └── gateway.go
│   └── harvester/              # Harvester API client
│       ├── client.go           # KubeVirt, CDI, Kube-OVN, monitoring
│       └── cloudinit.go        # PostgreSQL cloud-init generator
├── config/
│   ├── crd/                    # CRD YAML manifests
│   ├── rbac/                   # ServiceAccount, ClusterRole, ClusterRoleBinding
│   ├── manager/                # Controller Deployment + Service
│   └── samples/                # Example DBInstance, test VPC, demo app
├── main.go                     # Manager setup, gateway goroutine
├── Dockerfile                  # Multi-stage (native cross-compile)
├── go.mod
└── INSTALL.md                  # Step-by-step cluster install guide
```
