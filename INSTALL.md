# Installing the DBaaS Controller on Harvester

Step-by-step install sequence for a Harvester 1.7.1 cluster. The controller image (`wso2vick/ocd-dbaas:v0.1.0`) is published on Docker Hub and pulled at deploy time, so no local build is required.

## Prerequisites

- Harvester HCI 1.7.1 cluster (KubeVirt, CDI, and Kube-OVN are bundled)
- `kubectl` installed and configured to the Harvester cluster
- VM image `ubuntu-22.04-server-cloudimg-amd64.img` loaded in the Harvester image store
- (Optional) Prometheus Operator for monitoring flows
- (Optional) S3/MinIO endpoint for pgBackRest backups

## 1. Point kubectl at the cluster

```bash
export KUBECONFIG=/path/to/harvester-kubeconfig.yaml
kubectl cluster-info
```

## 2. Install the CRDs

```bash
kubectl apply -f config/crd/
kubectl get crd | grep dbaas.wso2.com
```

Expected output:

```
dbinstances.dbaas.wso2.com         <created>
dbparametergroups.dbaas.wso2.com   <created>
dbsnapshots.dbaas.wso2.com         <created>
```

## 3. Install namespace, ServiceAccount, and RBAC

```bash
kubectl apply -f config/rbac/
```

This creates the `dbaas-system` namespace, the `dbaas-controller` ServiceAccount, a ClusterRole scoped to the Harvester APIs the controller needs (KubeVirt, CDI, Kube-OVN, core, monitoring), and the ClusterRoleBinding that ties them together.

## 4. Install the controller Deployment + gateway Service

```bash
kubectl apply -f config/manager/
```

## 5. Wait for the controller to come up

```bash
kubectl -n dbaas-system rollout status deploy/dbaas-controller --timeout=120s
kubectl -n dbaas-system get pods
kubectl -n dbaas-system logs deploy/dbaas-controller --tail=50
```

## 6. Smoke-test with a sample database

```bash
kubectl apply -f config/samples/dbinstance.yaml
kubectl get dbi -w
```

You should see the instance walk through the provisioning phases:

```
NAME          PHASE       CLASS          ENDPOINT      AGE
orders-prod   creating    db.m5.large                  5s
orders-prod   creating    db.m5.large                  20s   # NamespaceCreated → StorageProvisioned
orders-prod   creating    db.m5.large   10.100.42.5    90s   # DatabaseReady
orders-prod   available   db.m5.large   10.100.42.5    95s   # Available
```

## Post-install operations

```bash
# Watch a specific instance
kubectl describe dbi orders-prod

# Get the JDBC URL
kubectl get dbi orders-prod -o jsonpath='{.status.endpoint.jdbcUrl}'

# Get the generated admin password
kubectl get secret pg-orders-prod-credentials -n dbaas-orders-prod \
  -o jsonpath='{.data.admin_password}' | base64 -d

# Port-forward the REST gateway for local testing
kubectl -n dbaas-system port-forward svc/dbaas-gateway 8080:8080
# then: curl http://localhost:8080/rds/v1/db-instances
```

## Uninstall

```bash
# Remove sample DBs first (finalizer-driven teardown of VMs, storage, network)
kubectl delete dbi --all

# Then the controller stack
kubectl delete -f config/manager/
kubectl delete -f config/rbac/
kubectl delete -f config/crd/
```

## Notes

- The CRDs use `x-kubernetes-preserve-unknown-fields: true` on `spec`/`status`. They are functional but not strict. Running `controller-gen` against `api/v1alpha1` will regenerate fully-typed schemas.
- The REST gateway has no built-in authentication. Do not expose it outside the cluster without placing an auth-enforcing ingress or API gateway in front of it. See the Trust Model section in `README.md`.
- Upgrading the controller image: edit `config/manager/manager.yaml` (or `kubectl -n dbaas-system set image deploy/dbaas-controller controller=wso2vick/ocd-dbaas:<new-tag>`) and re-apply.
