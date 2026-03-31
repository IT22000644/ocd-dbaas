# DBaaS Controller for Harvester HCI

A Kubernetes-native Database-as-a-Service that provisions managed PostgreSQL instances on [Harvester HCI](https://harvesterhci.io/) v1.7.1.

**Architecture:** Async CRD + Controller model — the REST API creates CRDs and returns immediately (`HTTP 202`), a controller reconciles desired state to Harvester via phase-based provisioning.

## Features

- **RDS-compatible REST API** — field names, response shapes, and status strings match AWS RDS
- **Async CRD model** — `kubectl apply` a `DBInstance` YAML or `POST` to the REST API; controller handles the rest
- **Phase-based reconciliation** — crash-safe, idempotent, resumable from any step
- **LUKS2 encryption at rest** — every PGDATA volume is encrypted via cloud-init
- **SSL-only connections** — self-signed CA, `pg_hba.conf` rejects all non-SSL
- **Kube-OVN VPC isolation** — each database gets its own VPC with controlled cross-VLAN access
- **Prometheus + Grafana monitoring** — auto-deployed ServiceMonitor and dashboard per instance
- **pgBackRest backups to S3** — configurable retention and schedule
- **Stop/Start** — pause instances without deleting (maps to KubeVirt `spec.running`)
- **Live resize** — change instance class or storage size on a running database
- **Deletion protection** — prevents accidental `kubectl delete`

## Quick Start

### Prerequisites

- Harvester HCI 1.7.1 cluster
- Kube-OVN enabled (for VPC isolation)
- `kubectl` configured to the Harvester cluster
- VM image `ubuntu-22.04-server-cloudimg-amd64.img` in the Harvester image store
- (Optional) Prometheus Operator for monitoring
- (Optional) S3/MinIO endpoint for backups

### Install

```bash
# Validate the source locally
go test ./...
go build ./...

# Apply the sample custom resource after your CRD/controller manifests are installed
kubectl apply -f config/samples/dbinstance.yaml
```

Generated deployment manifests such as `config/crd/`, `config/rbac/`, and `config/manager/` are not currently checked into this repository.

### Required Permissions

External callers of the REST API should not be given direct Harvester credentials. Instead, run the controller/gateway pod with a dedicated Kubernetes `ServiceAccount`, and grant that service account the permissions it needs to create and manage Harvester resources on behalf of API callers.

Because this controller manages a cluster-scoped `DBInstance` CRD and also creates cluster-scoped resources such as namespaces, Kube-OVN VPCs, and subnets, the practical deployment model is a `ClusterRole` plus `ClusterRoleBinding`.

The following example is the minimum practical RBAC for the code in this repository:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: dbaas-controller
  namespace: dbaas-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: dbaas-controller
rules:
- apiGroups: ["dbaas.wso2.com"]
  resources: ["dbinstances"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
- apiGroups: ["dbaas.wso2.com"]
  resources: ["dbinstances/status"]
  verbs: ["get", "update", "patch"]
- apiGroups: ["dbaas.wso2.com"]
  resources: ["dbinstances/finalizers"]
  verbs: ["update"]
- apiGroups: [""]
  resources: ["namespaces"]
  verbs: ["get", "create"]
- apiGroups: ["kubeovn.io"]
  resources: ["vpcs"]
  verbs: ["get", "create", "update", "delete"]
- apiGroups: ["kubeovn.io"]
  resources: ["subnets"]
  verbs: ["create", "delete"]
- apiGroups: ["kubeovn.io"]
  resources: ["vpc-peerings"]
  verbs: ["get", "create", "delete"]
- apiGroups: ["k8s.cni.cncf.io"]
  resources: ["network-attachment-definitions"]
  verbs: ["create", "delete"]
- apiGroups: ["cdi.kubevirt.io"]
  resources: ["datavolumes"]
  verbs: ["get", "create", "update", "delete"]
- apiGroups: ["kubevirt.io"]
  resources: ["virtualmachines"]
  verbs: ["get", "create", "update", "delete"]
- apiGroups: ["kubevirt.io"]
  resources: ["virtualmachineinstances"]
  verbs: ["get"]
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["create", "delete"]
- apiGroups: [""]
  resources: ["services"]
  verbs: ["create"]
- apiGroups: ["monitoring.coreos.com"]
  resources: ["servicemonitors"]
  verbs: ["create", "delete"]
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: dbaas-controller
subjects:
- kind: ServiceAccount
  name: dbaas-controller
  namespace: dbaas-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: dbaas-controller
```

Notes:

- `secrets` access is used to create the generated database credentials and LUKS encryption key.
- `services` and `servicemonitors` are only needed if you enable the built-in monitoring flow.
- `leases` are only needed because leader election is enabled by default.
- External API authorization is a separate concern: if this API is exposed outside the cluster, place it behind your normal auth layer (for example, an ingress or API gateway with user authentication and authorization).

### Trust Model

This design uses two separate trust relationships:

1. Client to DBaaS API
2. DBaaS API/controller to Harvester

Clients should trust the DBaaS API through normal HTTPS and whatever authentication/authorization layer you place in front of it.

The DBaaS API/controller should trust Harvester through the Kubernetes API server, using:

- TLS to the cluster API endpoint
- the cluster CA certificate to verify the API server
- a dedicated Kubernetes `ServiceAccount` token for authentication
- RBAC for authorization

In other words, external callers should not be given Harvester credentials directly. The controller pod should run with a scoped service account, and Harvester should allow only that identity to create and manage the resources listed above.

The code in this repository already follows that model by loading cluster configuration and creating Kubernetes clients from it.

Recommended deployment pattern:

- run the controller and REST gateway inside the Harvester cluster
- assign `serviceAccountName: dbaas-controller` to the pod
- expose the REST API separately behind TLS and your preferred auth layer

Example Deployment snippet:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dbaas-controller
  namespace: dbaas-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: dbaas-controller
  template:
    metadata:
      labels:
        app: dbaas-controller
    spec:
      serviceAccountName: dbaas-controller
      containers:
      - name: controller
        image: your-registry/dbaas-controller:latest
        args:
        - --gateway-address=:8080
        - --metrics-bind-address=:8081
        - --health-probe-bind-address=:8082
```

When running inside the cluster, Kubernetes automatically provides the service account token and cluster CA certificate to the pod. The controller uses those credentials to call the Harvester/Kubernetes API securely.

If you run the API outside the cluster, provide a dedicated low-privilege `kubeconfig` that contains:

- the Harvester API server URL
- the cluster CA certificate
- credentials for a tightly scoped identity

### Caller Authentication

The REST API should authenticate every caller before accepting create, modify, stop/start, or delete requests.

Recommended approach:

- terminate TLS at your ingress or API gateway
- require an OAuth2/OIDC bearer token for each request
- validate the token before forwarding traffic to the DBaaS API
- authorize actions based on the caller identity, group, tenant, or namespace policy

Good production options include:

- OIDC/JWT for user or service authentication
- mTLS for service-to-service calls inside a private network
- an API gateway or ingress controller that enforces auth centrally

Recommended behavior:

- return `401 Unauthorized` when the caller is missing or has an invalid token
- return `403 Forbidden` when the caller is authenticated but is not allowed to manage the requested database

Important:

- callers should only be authenticated to the DBaaS API
- callers should not receive Harvester credentials directly
- the DBaaS controller should continue to use its own Kubernetes `ServiceAccount` when talking to Harvester

The examples below assume a bearer token is required by the external API.

### Create a Database

**Option A: kubectl (GitOps-friendly)**

```yaml
apiVersion: dbaas.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: orders-prod
spec:
  dbInstanceClass: db.m5.large    # 2 CPU, 8GB RAM
  allocatedStorage: 100           # 100 GiB
  dbName: orders
  masterUsername: orders_admin
  manageMasterUserPassword: true
  dbSubnetGroupName: "10.50.0.0/24"
  backupRetentionPeriod: 7
  deletionProtection: true
```

```bash
kubectl apply -f config/samples/dbinstance.yaml

# Watch provisioning progress
kubectl get dbi orders-prod -w

# Output:
# NAME          STATUS    PHASE                CLASS          ENDPOINT        AGE
# orders-prod   creating  NamespaceCreated     db.m5.large                    5s
# orders-prod   creating  NetworkProvisioned   db.m5.large                    8s
# orders-prod   creating  StorageProvisioned   db.m5.large                    15s
# orders-prod   creating  VMCreated            db.m5.large                    20s
# orders-prod   creating  WaitingForCloudInit  db.m5.large                    25s
# orders-prod   creating  DatabaseReady        db.m5.large    10.100.42.5     90s
# orders-prod   available Available            db.m5.large    10.100.42.5     95s
```

**Option B: External REST API**

```bash
DBAAS_API=http://dbaas-controller.dbaas-system:8080/rds/v1/db-instances
DBAAS_TOKEN=<your-access-token>

# Create a database
curl -X POST "${DBAAS_API}" \
  -H "Authorization: Bearer ${DBAAS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "DBInstanceIdentifier": "orders-prod",
    "DBInstanceClass": "db.m5.large",
    "AllocatedStorage": 100,
    "DBName": "orders",
    "MasterUsername": "orders_admin",
    "DBSubnetGroupName": "10.50.0.0/24",
    "BackupRetentionPeriod": 7,
    "DeletionProtection": true
  }'

# Example HTTP 202 response:
# {
#   "DBInstance": {
#     "DBInstanceIdentifier": "orders-prod",
#     "DBInstanceStatus": "creating"
#   }
# }

# Poll for status:
curl "${DBAAS_API}/orders-prod" \
  -H "Authorization: Bearer ${DBAAS_TOKEN}"

# Example HTTP 200 response while provisioning:
# {
#   "DBInstance": {
#     "DBInstanceIdentifier": "orders-prod",
#     "DBInstanceStatus": "creating"
#   }
# }

# Example HTTP 200 response when available:
# {
#   "DBInstance": {
#     "DBInstanceIdentifier": "orders-prod",
#     "DBInstanceStatus": "available",
#     "Endpoint": {
#       "Address": "10.100.42.5",
#       "Port": 5432,
#       "JDBCURL": "jdbc:postgresql://10.100.42.5:5432/orders?ssl=true&sslmode=verify-full"
#     }
#   }
# }

# Update an existing database (resize class/storage, change backup window)
curl -X PATCH "${DBAAS_API}/orders-prod" \
  -H "Authorization: Bearer ${DBAAS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "DBInstanceClass": "db.m5.xlarge",
    "AllocatedStorage": 200,
    "BackupRetentionPeriod": 14,
    "PreferredBackupWindow": "02:00-03:00"
  }'

# Example HTTP 202 response:
# {
#   "DBInstance": {
#     "DBInstanceIdentifier": "orders-prod",
#     "DBInstanceStatus": "modifying"
#   }
# }

# Stop the database through the external API
curl -X PATCH "${DBAAS_API}/orders-prod" \
  -H "Authorization: Bearer ${DBAAS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "Running": false
  }'

# Example HTTP 202 response:
# {
#   "DBInstance": {
#     "DBInstanceIdentifier": "orders-prod",
#     "DBInstanceStatus": "stopping"
#   }
# }

# Start it again
curl -X PATCH "${DBAAS_API}/orders-prod" \
  -H "Authorization: Bearer ${DBAAS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "Running": true
  }'

# Example HTTP 202 response:
# {
#   "DBInstance": {
#     "DBInstanceIdentifier": "orders-prod",
#     "DBInstanceStatus": "starting"
#   }
# }

# Delete requires deletion protection to be disabled first
curl -X PATCH "${DBAAS_API}/orders-prod" \
  -H "Authorization: Bearer ${DBAAS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "DeletionProtection": false
  }'

# Delete the database
curl -X DELETE "${DBAAS_API}/orders-prod" \
  -H "Authorization: Bearer ${DBAAS_TOKEN}"

# Example HTTP 202 response:
# {
#   "DBInstance": {
#     "DBInstanceIdentifier": "orders-prod",
#     "DBInstanceStatus": "deleting"
#   }
# }
```

`POST`, `PATCH`, and `DELETE` are asynchronous: the gateway accepts the request, updates the backing `DBInstance`, and the controller reconciles the change in the background. Use `GET /rds/v1/db-instances/{id}` to watch the latest status. In production, these endpoints should be protected by caller authentication such as OIDC bearer tokens or mTLS.

### Connect to the Database

```bash
# Get the JDBC URL
kubectl get dbi orders-prod -o jsonpath='{.status.endpoint.jdbcUrl}'
# jdbc:postgresql://10.100.42.5:5432/orders?ssl=true&sslmode=verify-full

# Get the admin password
kubectl get secret pg-orders-prod-credentials -n dbaas-orders-prod \
  -o jsonpath='{.data.admin_password}' | base64 -d

# Get the CA certificate for SSL verification
kubectl get dbi orders-prod -o jsonpath='{.status.caCertPem}' > ca.crt
```

### Operations

```bash
# Stop (preserves storage, frees compute)
kubectl patch dbi orders-prod --type merge -p '{"spec":{"running":false}}'

# Start
kubectl patch dbi orders-prod --type merge -p '{"spec":{"running":true}}'

# Resize (live)
kubectl patch dbi orders-prod --type merge -p \
  '{"spec":{"dbInstanceClass":"db.m5.xlarge","allocatedStorage":200}}'

# Delete (must disable protection first)
kubectl patch dbi orders-prod --type merge -p '{"spec":{"deletionProtection":false}}'
kubectl delete dbi orders-prod
```

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│  REST API Gateway (:8080)                                │
│  POST/PATCH/DELETE → manage DBInstance CRDs async        │
│  GET               → read CRD status → RDS-style output  │
└──────────────┬───────────────────────────────────────────┘
               │ kubectl apply / HTTP POST
┌──────────────▼───────────────────────────────────────────┐
│  Kubernetes CRDs (etcd)                                  │
│  ┌──────────────┐ ┌────────────┐ ┌──────────────────┐    │
│  │ DBInstance    │ │ DBSnapshot │ │ DBParameterGroup │    │
│  │ spec + status │ │            │ │                  │    │
│  └──────────────┘ └────────────┘ └──────────────────┘    │
└──────────────┬───────────────────────────────────────────┘
               │ watch + reconcile loop
┌──────────────▼───────────────────────────────────────────┐
│  DBInstance Controller (controller-runtime)               │
│  Phase: Namespace → Network → Storage → VM → DB → Mon    │
│  Each phase: idempotent, retryable, resumable             │
└──────────────┬───────────────────────────────────────────┘
               │ Harvester APIs (per phase)
┌──────────────▼───────────────────────────────────────────┐
│  Harvester 1.7.1                                         │
│  ┌──────────┐ ┌───────────┐ ┌──────────┐ ┌───────────┐  │
│  │ Kube-OVN │ │ CDI       │ │ KubeVirt │ │ Prometheus│  │
│  │ VPC      │ │ DataVol   │ │ VM       │ │ + Grafana │  │
│  └──────────┘ └───────────┘ └──────────┘ └───────────┘  │
└──────────────────────────────────────────────────────────┘
```

### Network Topology

The following diagram shows how a single database instance is placed inside its own Kube-OVN VPC and subnet, while an external consumer VLAN is allowed to connect to PostgreSQL.

```
Application / client workloads
Consumer VLAN / CIDR from `DBSubnetGroupName`
Example: 10.50.0.0/24
                 │
                 │ PostgreSQL traffic to TCP/5432
                 │
                 ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Kube-OVN VPC: dbaas-orders-prod-vpc                                │
│ Static route allows traffic from the consumer VLAN into the VPC    │
│ Namespaces attached to this VPC: [dbaas-orders-prod]               │
│                                                                     │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │ Kube-OVN Subnet: dbaas-orders-prod-subnet                    │  │
│  │ CIDR: 10.100.X.0/24                                          │  │
│  │ Gateway: 10.100.X.1                                           │  │
│  │ allowSubnets: [10.50.0.0/24]                                  │  │
│  │                                                               │  │
│  │  Namespace: dbaas-orders-prod                                 │  │
│  │                                                               │  │
│  │  ┌─────────────────────────────────────────────────────────┐  │  │
│  │  │ NetworkAttachmentDefinition: dbaas-orders-prod-nad     │  │  │
│  │  │ Multus / Kube-OVN attachment into the subnet           │  │  │
│  │  └─────────────────────────────────────────────────────────┘  │  │
│  │                                                               │  │
│  │  ┌─────────────────────────────────────────────────────────┐  │  │
│  │  │ KubeVirt VM: pg-orders-prod                            │  │  │
│  │  │ NIC: vpc-net                                           │  │  │
│  │  │ IP: 10.100.X.Y                                         │  │  │
│  │  │ PostgreSQL listens on port 5432                        │  │  │
│  │  │ PGDATA stored on encrypted CDI DataVolume              │  │  │
│  │  └─────────────────────────────────────────────────────────┘  │  │
│  └───────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

Key points:

- Each database instance gets its own VPC, subnet, NAD, namespace, and PostgreSQL VM.
- `DBSubnetGroupName` is used here as the external consumer VLAN or CIDR that is allowed to reach the database.
- The VM is attached to the Kube-OVN subnet through a `NetworkAttachmentDefinition`, so the database endpoint is the VM IP inside the VPC subnet.
- Client traffic reaches the database through the VPC route and the subnet `allowSubnets` rule, not through a public LoadBalancer.
- Monitoring is separate from the data path: the controller can also create a metrics `Service` and `ServiceMonitor`, but application traffic goes directly to the PostgreSQL VM endpoint.

### VPC Peering

When application pods run in a separate VPC (for example, an RKE2 cluster), they cannot reach the database VPC by default. The optional `vpcPeering` field in the `DBInstance` spec creates a Kube-OVN `VpcPeering` resource that establishes bidirectional routing between the two VPCs.

The following diagram uses the `orders-prod` sample, where the RKE2 application cluster lives in `rke2-cluster-vpc` and the database is provisioned in `dbaas-orders-prod-vpc`:

```
┌─────────────────────────────────────────┐       ┌─────────────────────────────────────────┐
│  RKE2 Cluster VPC: rke2-cluster-vpc     │       │  DBaaS VPC: dbaas-orders-prod-vpc       │
│                                         │       │                                         │
│  ┌───────────────────────────────────┐  │       │  ┌───────────────────────────────────┐  │
│  │ Subnet: rke2-cluster-subnet       │  │       │  │ Subnet: dbaas-orders-prod-subnet  │  │
│  │ CIDR: 10.16.0.0/16               │  │       │  │ CIDR: 10.100.X.0/24              │  │
│  │                                   │  │       │  │                                   │  │
│  │  ┌─────────────────────────────┐  │  │       │  │  ┌─────────────────────────────┐  │  │
│  │  │ App Pod: orders-service     │  │  │       │  │  │ KubeVirt VM: pg-orders-prod │  │  │
│  │  │ IP: 10.16.5.12              │  │  │       │  │  │ IP: 10.100.X.Y              │  │  │
│  │  │                             │  │  │       │  │  │                             │  │  │
│  │  │ psql -h 10.100.X.Y ──────────────────────────▶│ PostgreSQL :5432             │  │  │
│  │  │                             │  │  │       │  │  │ (SSL-only, LUKS-encrypted)  │  │  │
│  │  └─────────────────────────────┘  │  │       │  │  └─────────────────────────────┘  │  │
│  │                                   │  │       │  │                                   │  │
│  └───────────────────────────────────┘  │       │  └───────────────────────────────────┘  │
│                                         │       │                                         │
└──────────────────┬──────────────────────┘       └──────────────────┬──────────────────────┘
                   │                                                 │
                   │       Kube-OVN VpcPeering                       │
                   │       dbaas-orders-prod-peering                 │
                   │                                                 │
                   │  ┌───────────────────────────────────────────┐  │
                   └──│ localVpc:     dbaas-orders-prod-vpc       │──┘
                      │ remoteVpc:    rke2-cluster-vpc            │
                      │ localSubnets: [dbaas-orders-prod-subnet]  │
                      │ remoteSubnets:[rke2-cluster-subnet]       │
                      └───────────────────────────────────────────┘
```

How it works:

1. The controller creates the DBaaS VPC and subnet during normal provisioning (phase `NetworkProvisioned`).
2. After monitoring is deployed, the `VpcPeering` phase creates a `VpcPeering` resource named `dbaas-{id}-peering`.
3. Kube-OVN installs bidirectional routes so that pods in `rke2-cluster-subnet` can reach IPs in `dbaas-orders-prod-subnet` and vice versa.
4. Application pods connect directly to the PostgreSQL VM IP — no LoadBalancer or NodePort required.

To enable VPC peering in a `DBInstance`, add the `vpcPeering` block to the spec:

```yaml
spec:
  vpcPeering:
    remoteVpc: rke2-cluster-vpc
    remoteSubnet: rke2-cluster-subnet
```

If `vpcPeering` is omitted, the database is only reachable from the consumer VLAN specified by `dbSubnetGroupName`.

### Provisioning Phases

The controller advances one phase per reconcile loop iteration:

| Phase | What it does | Harvester API |
|-------|-------------|---------------|
| Pending → NamespaceCreated | Creates `dbaas-{id}` namespace | `POST /api/v1/namespaces` |
| → NetworkProvisioned | Creates Kube-OVN VPC + Subnet + NAD | `POST kubeovn.io/v1/vpcs`, `subnets`, `k8s.cni.cncf.io/v1/network-attachment-definitions` |
| → StorageProvisioned | Creates CDI DataVolume (blank block for LUKS) | `POST cdi.kubevirt.io/v1beta1/datavolumes` |
| → VMCreated | Creates KubeVirt VM with cloud-init + credentials Secret | `POST kubevirt.io/v1/virtualmachines`, `v1/secrets` |
| → WaitingForCloudInit | Watches VMI status for Running + IP | `WATCH kubevirt.io/v1/virtualmachineinstances` |
| → DatabaseReady | PostgreSQL accepts connections | KubeVirt exec subresource |
| → MonitoringDeployed | Creates ServiceMonitor + Grafana dashboard | `POST monitoring.coreos.com/v1/servicemonitors` |
| → Available | Done. Endpoint populated. | — |

**Crash recovery:** If the controller restarts, it reads `status.provisioningPhase` and `status.resources` from the CRD to determine what's already created, and resumes from the next phase.

### Instance Classes

| Class | vCPU | RAM | Max Connections |
|-------|------|-----|----------------|
| db.t3.micro | 1 | 1 GB | 50 |
| db.t3.small | 1 | 2 GB | 100 |
| db.t3.medium | 2 | 4 GB | 150 |
| db.t3.large | 2 | 8 GB | 200 |
| db.m5.large | 2 | 8 GB | 200 |
| db.m5.xlarge | 4 | 16 GB | 400 |
| db.m5.2xlarge | 8 | 32 GB | 600 |
| db.m5.4xlarge | 16 | 64 GB | 1000 |
| db.r5.large | 2 | 16 GB | 300 |
| db.r5.xlarge | 4 | 32 GB | 500 |
| db.r5.2xlarge | 8 | 64 GB | 800 |

## Development

```bash
# Build
make build

# Run locally against your kubeconfig
make install   # install CRDs
make run       # start controller + REST gateway

# Test
make smoke-test  # creates a database via REST API
make status      # kubectl get dbi
```

## Project Structure

```
dbaas/
├── api/v1alpha1/           # CRD Go types + deepcopy
│   ├── types.go            # DBInstance, DBSnapshot, DBParameterGroup
│   ├── groupversion_info.go
│   └── zz_generated.deepcopy.go
├── cmd/controller/         # Main entry point
│   └── main.go             # Starts controller-runtime manager + REST gateway
├── internal/
│   ├── controller/         # Reconciler (the core async logic)
│   │   └── dbinstance_reconciler.go
│   ├── gateway/            # Thin REST API → CRD translator
│   │   └── handler.go
│   └── harvester/          # Harvester API client (KubeVirt, CDI, Kube-OVN)
│       ├── client.go       # Dynamic client wrapper
│       └── cloudinit.go    # PostgreSQL cloud-init generator
├── config/
│   ├── crd/                # CRD YAML manifests
│   ├── rbac/               # ServiceAccount, ClusterRole, ClusterRoleBinding
│   ├── manager/            # Controller Deployment + Service
│   └── samples/            # Example DBInstance YAMLs
├── Dockerfile
├── Makefile
└── go.mod
```

## Part of Open Cloud Datacenter

This component is designed to fit into the [WSO2 Open Cloud Datacenter](https://github.com/wso2/open-cloud-datacenter) initiative, providing managed database services on Harvester HCI for Choreo and Asgardeo workloads.

## License

Apache-2.0
