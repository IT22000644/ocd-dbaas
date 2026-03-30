# REST Gateway API Reference

The DBaaS controller includes a lightweight HTTP REST gateway (`internal/gateway/gateway.go`) that exposes DBInstance CRUD operations. It runs as a goroutine alongside the controller manager and uses the controller-runtime `client.Client` to read/write CRDs directly.

**Default address:** `:8080` (configurable via `--gateway-address`)

> **Note:** The gateway currently has no authentication, TLS, or rate limiting. Place it behind an ingress or API gateway with your auth layer for production use.

---

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/healthz` | Health check |
| `GET` | `/dbinstances` | List all DBInstances |
| `POST` | `/dbinstances` | Create a new DBInstance |
| `GET` | `/dbinstances/{name}` | Get a single DBInstance |
| `DELETE` | `/dbinstances/{name}` | Delete a DBInstance |
| `POST` | `/dbinstances/{name}/stop` | Stop a running instance |
| `POST` | `/dbinstances/{name}/start` | Start a stopped instance |

All responses are `Content-Type: application/json`.

---

## Health Check

```
GET /healthz
```

**Response:** `200 OK`
```json
{"status": "ok"}
```

---

## List All Instances

```
GET /dbinstances
```

Returns the full `DBInstanceList` CRD object.

**Response:** `200 OK`
```json
{
  "apiVersion": "dbaas.wso2.com/v1alpha1",
  "kind": "DBInstanceList",
  "metadata": {},
  "items": [
    {
      "metadata": { "name": "orders-prod" },
      "spec": {
        "dbInstanceClass": "db.m5.large",
        "allocatedStorage": 100,
        "dbName": "orders"
      },
      "status": {
        "phase": "available",
        "provisioningPhase": "Available",
        "endpoint": {
          "address": "10.142.87.5",
          "port": 5432,
          "jdbcUrl": "jdbc:postgresql://10.142.87.5:5432/orders?ssl=true&sslmode=verify-full"
        }
      }
    }
  ]
}
```

---

## Create an Instance

```
POST /dbinstances
Content-Type: application/json
```

The request body is a `DBInstance` CRD object. `apiVersion` and `kind` are auto-filled if omitted. `metadata.name` is required.

**Request:**
```json
{
  "metadata": {
    "name": "orders-prod"
  },
  "spec": {
    "dbInstanceClass": "db.m5.large",
    "allocatedStorage": 100,
    "dbName": "orders",
    "masterUsername": "orders_admin",
    "manageMasterUserPassword": true,
    "dbSubnetGroupName": "10.50.0.0/24",
    "backupRetentionPeriod": 7,
    "deletionProtection": true
  }
}
```

**Response:** `202 Accepted`
```json
{
  "apiVersion": "dbaas.wso2.com/v1alpha1",
  "kind": "DBInstance",
  "metadata": {
    "name": "orders-prod",
    "creationTimestamp": "2026-03-30T10:00:00Z"
  },
  "spec": { "..." : "..." },
  "status": {}
}
```

**Errors:**
- `400` — missing `metadata.name` or invalid JSON
- `409` — instance with that name already exists

---

## Get an Instance

```
GET /dbinstances/{name}
```

**Response:** `200 OK`
```json
{
  "apiVersion": "dbaas.wso2.com/v1alpha1",
  "kind": "DBInstance",
  "metadata": { "name": "orders-prod" },
  "spec": {
    "dbInstanceClass": "db.m5.large",
    "allocatedStorage": 100,
    "dbName": "orders",
    "port": 5432
  },
  "status": {
    "phase": "available",
    "provisioningPhase": "Available",
    "endpoint": {
      "address": "10.142.87.5",
      "port": 5432,
      "jdbcUrl": "jdbc:postgresql://10.142.87.5:5432/orders?ssl=true&sslmode=verify-full"
    },
    "masterUserSecret": {
      "name": "pg-orders-prod-credentials",
      "status": "active"
    },
    "resources": {
      "namespace": "dbaas-orders-prod",
      "vpcName": "dbaas-orders-prod-vpc",
      "subnetName": "dbaas-orders-prod-subnet",
      "vmName": "pg-orders-prod"
    },
    "grafanaUrl": "https://grafana.monitoring.svc/d/dbaas-orders-prod/postgresql-orders-prod",
    "prometheusTarget": "pg-orders-prod-metrics.dbaas-orders-prod.svc:9187",
    "message": "Database instance is available"
  }
}
```

**Errors:**
- `404` — instance not found

---

## Delete an Instance

```
DELETE /dbinstances/{name}
```

Triggers the controller's finalizer-based teardown: VM, DataVolume, network, secrets, monitoring, and namespace are all cleaned up.

**Response:** `202 Accepted`
```json
{
  "status": "deletion requested",
  "name": "orders-prod"
}
```

**Errors:**
- `404` — instance not found

> **Note:** If `spec.deletionProtection` is `true`, the controller will reject the deletion and set `status.message` to explain why. Disable it first via kubectl: `kubectl patch dbi orders-prod --type merge -p '{"spec":{"deletionProtection":false}}'`

---

## Stop an Instance

```
POST /dbinstances/{name}/stop
```

Sets `spec.running = false`. The controller shuts down the KubeVirt VM while preserving storage.

**Response:** `202 Accepted` — returns the updated DBInstance object.

The instance transitions through phases:
1. `status.phase` → `"stopping"`
2. `status.phase` → `"stopped"`

**Errors:**
- `404` — instance not found

---

## Start an Instance

```
POST /dbinstances/{name}/start
```

Sets `spec.running = true`. The controller boots the KubeVirt VM.

**Response:** `202 Accepted` — returns the updated DBInstance object.

The instance transitions through phases:
1. `status.phase` → `"starting"`
2. `status.phase` → `"available"`

**Errors:**
- `404` — instance not found

---

## Status Phase Values

These are the possible values for `status.phase` (RDS-compatible lowercase strings):

| Phase | Meaning |
|-------|---------|
| `creating` | Initial provisioning in progress |
| `available` | Database is ready for connections |
| `stopping` | VM is shutting down |
| `stopped` | VM is off, storage preserved |
| `starting` | VM is booting |
| `modifying` | Resize or config change in progress |
| `deleting` | Teardown in progress |
| `failed` | An error occurred (see `status.message`) |

---

## Provisioning Phase Values

`status.provisioningPhase` tracks the internal reconciler step (PascalCase):

| Phase | What completed |
|-------|---------------|
| `Pending` | Initial state |
| `NamespaceCreated` | `dbaas-{name}` namespace created |
| `NetworkProvisioned` | Kube-OVN VPC + Subnet + NAD created |
| `StorageProvisioned` | CDI DataVolume created |
| `VMCreated` | KubeVirt VM + credentials Secret created |
| `WaitingForCloudInit` | VM running, PostgreSQL initializing |
| `DatabaseReady` | PostgreSQL accepting connections |
| `MonitoringDeployed` | ServiceMonitor + metrics Service created |
| `Available` | Fully reconciled |
| `Failed` | Error (retries every 30s) |

---

## Example: Full Lifecycle with curl

```bash
GATEWAY=http://localhost:8080

# Create
curl -s -X POST "$GATEWAY/dbinstances" \
  -H "Content-Type: application/json" \
  -d '{
    "metadata": {"name": "mydb"},
    "spec": {
      "dbInstanceClass": "db.t3.medium",
      "allocatedStorage": 50,
      "dbName": "myapp",
      "dbSubnetGroupName": "10.50.0.0/24"
    }
  }' | jq .

# Poll status
curl -s "$GATEWAY/dbinstances/mydb" | jq '.status.phase, .status.provisioningPhase'

# Stop
curl -s -X POST "$GATEWAY/dbinstances/mydb/stop" | jq .status.phase

# Start
curl -s -X POST "$GATEWAY/dbinstances/mydb/start" | jq .status.phase

# Delete
curl -s -X DELETE "$GATEWAY/dbinstances/mydb" | jq .
```
