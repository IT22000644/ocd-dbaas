# Future Improvements

Identified during validation on 2026-03-29. None are bugs in the current implementation.

## Medium Priority

### 1. Backup retention default mismatch
**File:** `api/v1alpha1/types.go:57`
The doc comment says "Default 7" but the reconciler treats `0` as disabled and never applies a default of 7. Users relying on the comment will get no backups.
**Fix:** Either apply the default in the reconciler or change the comment to "Default 0 (disabled)".

### 2. Static route duplication on retry
**File:** `internal/harvester/client.go:137-145`
On crash + re-reconcile, `CreateVPCNetwork` appends the consumer VLAN static route every time the VPC already exists. The VPC accumulates duplicate `staticRoutes` entries. Kube-OVN deduplicates at the OVS level so this is benign, but untidy.
**Fix:** Check if the route already exists before appending.

### 3. `ManageMasterUserPassword` / `MasterUserPasswordRef` not wired up
**File:** `api/v1alpha1/types.go:42-46`, `internal/controller/dbinstance_reconciler.go`
The spec declares `manageMasterUserPassword` and `masterUserPasswordRef` but the reconciler always auto-generates credentials. If a user sets `manageMasterUserPassword: false` with a `masterUserPasswordRef`, it is silently ignored.
**Fix:** Read the user-supplied secret when `manageMasterUserPassword` is false.

## Low Priority

### 4. `EngineVersion` field is unused
**File:** `api/v1alpha1/types.go:30`
The `engineVersion` spec field is never read by the reconciler or cloud-init. The VM gets whatever PostgreSQL version ships with the OS image.
**Fix:** Pass `EngineVersion` to cloud-init and install the specified major version, or remove the field.

### 5. No reconcilers for DBSnapshot / DBParameterGroup
**Files:** `api/v1alpha1/types.go:174, 208`
These CRDs are registered in the scheme with deepcopy but have no controllers. They are placeholder types for future features.
**Fix:** Implement snapshot and parameter group controllers when ready.

### 6. Gateway has no auth, TLS, or rate limiting
**File:** `internal/gateway/gateway.go`
The REST gateway exposes create/delete operations on cluster-scoped CRDs with no authentication, TLS termination, or rate limiting. This is an architectural decision that needs to be made before production use.
**Fix:** Add mTLS or token-based auth, and consider rate limiting. Alternatively, remove the gateway and rely solely on `kubectl` / the Kubernetes API.

### 7. Concurrent resize race (theoretical)
**File:** `internal/controller/dbinstance_reconciler.go:354-365`
`reconcileModify` runs `ResizeVM` and `ResizeDataVolume` concurrently. These operate on separate Kubernetes resources (VM vs DataVolume) so there is no conflict today. If future changes add concurrent mutations on the same resource, this pattern would need a mutex or sequential execution.
