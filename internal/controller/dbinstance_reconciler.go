package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/dbaas/internal/harvester"
)

// DBInstanceReconciler reconciles DBInstance CRDs.
// Each Reconcile call advances exactly one provisioning phase,
// updates the status, and requeues for the next phase.
type DBInstanceReconciler struct {
	client.Client
	Harvester *harvester.Client
}

// SetupWithManager registers the reconciler with controller-runtime.
func (r *DBInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbaasv1.DBInstance{}).
		Complete(r)
}

// Reconcile is the main entry point called by controller-runtime.
func (r *DBInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the DBInstance CRD
	var inst dbaasv1.DBInstance
	if err := r.Get(ctx, req.NamespacedName, &inst); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil // deleted
		}
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling", "name", inst.Name, "phase", inst.Status.ProvisioningPhase)

	// --- Handle deletion via finalizer ---
	if !inst.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&inst, dbaasv1.FinalizerName) {
			return r.reconcileDelete(ctx, &inst)
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present
	if !controllerutil.ContainsFinalizer(&inst, dbaasv1.FinalizerName) {
		controllerutil.AddFinalizer(&inst, dbaasv1.FinalizerName)
		if err := r.Update(ctx, &inst); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// --- Handle stop/start ---
	if inst.Spec.Running != nil && !*inst.Spec.Running && inst.Status.Phase == "available" {
		return r.reconcileStop(ctx, &inst)
	}
	if inst.Spec.Running != nil && *inst.Spec.Running && inst.Status.Phase == "stopped" {
		return r.reconcileStart(ctx, &inst)
	}

	// --- Handle spec changes on available instance ---
	if inst.Status.Phase == "available" && inst.Generation != inst.Status.ObservedGeneration {
		return r.reconcileModify(ctx, &inst)
	}

	// --- Phase-based provisioning ---
	switch inst.Status.ProvisioningPhase {
	case "", dbaasv1.PhasePending:
		return r.phaseNamespace(ctx, &inst)
	case dbaasv1.PhaseNamespaceCreated:
		return r.phaseNetwork(ctx, &inst)
	case dbaasv1.PhaseNetworkProvisioned:
		return r.phaseStorage(ctx, &inst)
	case dbaasv1.PhaseStorageProvisioned:
		return r.phaseVM(ctx, &inst)
	case dbaasv1.PhaseVMCreated, dbaasv1.PhaseWaitingForCloudInit:
		return r.phaseWaitReady(ctx, &inst)
	case dbaasv1.PhaseDatabaseReady:
		return r.phaseMonitoring(ctx, &inst)
	case dbaasv1.PhaseMonitoringDeployed:
		return r.phaseAvailable(ctx, &inst)
	case dbaasv1.PhaseAvailable:
		return ctrl.Result{}, nil // fully reconciled
	case dbaasv1.PhaseFailed:
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	default:
		return ctrl.Result{}, fmt.Errorf("unknown phase: %s", inst.Status.ProvisioningPhase)
	}
}

// ============================================================
// Provisioning phases
// ============================================================

func (r *DBInstanceReconciler) phaseNamespace(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	ns := fmt.Sprintf("dbaas-%s", inst.Name)

	// Idempotent: create namespace if not exists
	var existing corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: ns}, &existing); err != nil {
		if errors.IsNotFound(err) {
			nsObj := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   ns,
					Labels: map[string]string{"dbaas.wso2.com/instance": inst.Name},
				},
			}
			if err := r.Create(ctx, nsObj); err != nil && !errors.IsAlreadyExists(err) {
				return r.fail(ctx, inst, "NamespaceCreateFailed", err)
			}
		} else {
			return ctrl.Result{}, err
		}
	}

	inst.Status.Phase = "creating"
	inst.Status.ProvisioningPhase = dbaasv1.PhaseNamespaceCreated
	inst.Status.Resources.Namespace = ns
	inst.Status.Message = "Namespace created"

	return r.advance(ctx, inst)
}

func (r *DBInstanceReconciler) phaseNetwork(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	// Skip if already done
	if inst.Status.Resources.VPCName != "" {
		inst.Status.ProvisioningPhase = dbaasv1.PhaseNetworkProvisioned
		return r.advance(ctx, inst)
	}

	id := inst.Name
	ns := inst.Status.Resources.Namespace
	consumerVLAN := inst.Spec.DBSubnetGroupName
	if consumerVLAN == "" {
		consumerVLAN = "10.50.0.0/24"
	}
	port := inst.Spec.Port
	if port == 0 {
		port = 5432
	}

	vpcName, subnetName, nadName, err := r.Harvester.CreateVPCNetwork(ctx, id, ns, consumerVLAN, port)
	if err != nil {
		return r.fail(ctx, inst, "NetworkFailed", err)
	}

	inst.Status.Resources.VPCName = vpcName
	inst.Status.Resources.SubnetName = subnetName
	inst.Status.Resources.NADName = nadName
	inst.Status.ProvisioningPhase = dbaasv1.PhaseNetworkProvisioned
	inst.Status.Message = "VPC network provisioned"

	return r.advance(ctx, inst)
}

func (r *DBInstanceReconciler) phaseStorage(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	if inst.Status.Resources.DataVolumeName != "" {
		inst.Status.ProvisioningPhase = dbaasv1.PhaseStorageProvisioned
		return r.advance(ctx, inst)
	}

	id := inst.Name
	ns := inst.Status.Resources.Namespace
	storageType := inst.Spec.StorageType
	if storageType == "" {
		storageType = "longhorn"
	}

	dvName, err := r.Harvester.CreateDataVolume(ctx, id, ns, inst.Spec.AllocatedStorage, storageType)
	if err != nil {
		return r.fail(ctx, inst, "StorageFailed", err)
	}

	inst.Status.Resources.DataVolumeName = dvName
	inst.Status.ProvisioningPhase = dbaasv1.PhaseStorageProvisioned
	inst.Status.Message = "Encrypted storage provisioned"

	return r.advance(ctx, inst)
}

func (r *DBInstanceReconciler) phaseVM(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	if inst.Status.Resources.VMName != "" {
		inst.Status.ProvisioningPhase = dbaasv1.PhaseVMCreated
		return ctrl.Result{RequeueAfter: 10 * time.Second}, r.statusUpdate(ctx, inst)
	}

	id := inst.Name
	ns := inst.Status.Resources.Namespace

	classSpec, ok := dbaasv1.InstanceClasses[inst.Spec.DBInstanceClass]
	if !ok {
		return r.fail(ctx, inst, "InvalidClass", fmt.Errorf("unknown class: %s", inst.Spec.DBInstanceClass))
	}

	// Resolve defaults
	masterUser := inst.Spec.MasterUsername
	if masterUser == "" {
		masterUser = "dbadmin"
	}
	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = id
	}
	port := inst.Spec.Port
	if port == 0 {
		port = 5432
	}
	osImage := inst.Spec.OSImage
	if osImage == "" {
		osImage = "ubuntu-22.04-server-cloudimg-amd64.img"
	}

	// Generate cloud-init and credentials
	vmName, secretName, err := r.Harvester.CreatePostgresVM(ctx, harvester.VMCreateParams{
		ID:             id,
		Namespace:      ns,
		CPUCores:       classSpec.CPUCores,
		MemoryMB:       classSpec.MemoryMB,
		OSImage:        osImage,
		DataVolumeRef:  inst.Status.Resources.DataVolumeName,
		SubnetName:     inst.Status.Resources.SubnetName,
		NADName:        inst.Status.Resources.NADName,
		MasterUser:     masterUser,
		DBName:         dbName,
		Port:           port,
		MaxConnections: classSpec.MaxConnections,
		BackupEnabled:  inst.Spec.BackupRetentionPeriod > 0,
		BackupWindow:   inst.Spec.PreferredBackupWindow,
		S3Config:       inst.Spec.S3BackupConfig,
	})
	if err != nil {
		return r.fail(ctx, inst, "VMCreateFailed", err)
	}

	inst.Status.Resources.VMName = vmName
	inst.Status.Resources.SecretName = secretName
	inst.Status.MasterUserSecret = &dbaasv1.MasterUserSecretRef{
		Name:   secretName,
		Status: "active",
	}
	inst.Status.ProvisioningPhase = dbaasv1.PhaseVMCreated
	inst.Status.Message = "VM created, waiting for PostgreSQL to initialize"

	return ctrl.Result{RequeueAfter: 10 * time.Second}, r.statusUpdate(ctx, inst)
}

func (r *DBInstanceReconciler) phaseWaitReady(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	ns := inst.Status.Resources.Namespace

	// Check if the VM is running and has an IP
	ip, running, err := r.Harvester.GetVMIPAddress(ctx, ns, inst.Status.Resources.VMName)
	if err != nil || !running || ip == "" {
		inst.Status.Message = "Waiting for VM to become ready"
		inst.Status.ProvisioningPhase = dbaasv1.PhaseWaitingForCloudInit
		_ = r.statusUpdate(ctx, inst)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Check if PostgreSQL is accepting connections
	port := inst.Spec.Port
	if port == 0 {
		port = 5432
	}

	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = inst.Name
	}

	// Try to connect — if this succeeds, PG is ready
	pgReady := r.Harvester.CheckPostgresReady(ctx, ns, inst.Status.Resources.VMName, port)
	if !pgReady {
		inst.Status.Message = fmt.Sprintf("VM running at %s, waiting for PostgreSQL to finish initializing", ip)
		_ = r.statusUpdate(ctx, inst)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// Database is ready
	inst.Status.Endpoint = &dbaasv1.Endpoint{
		Address: ip,
		Port:    port,
		JDBCURL: fmt.Sprintf("jdbc:postgresql://%s:%d/%s?ssl=true&sslmode=verify-full", ip, port, dbName),
	}
	inst.Status.ProvisioningPhase = dbaasv1.PhaseDatabaseReady
	inst.Status.Message = "PostgreSQL is ready"

	return r.advance(ctx, inst)
}

func (r *DBInstanceReconciler) phaseMonitoring(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	if inst.Status.Resources.ServiceMonitor != "" {
		inst.Status.ProvisioningPhase = dbaasv1.PhaseMonitoringDeployed
		return r.advance(ctx, inst)
	}

	id := inst.Name
	ns := inst.Status.Resources.Namespace
	port := inst.Spec.Port
	if port == 0 {
		port = 5432
	}

	smName, grafanaURL, promTarget, err := r.Harvester.DeployMonitoring(ctx, id, ns, inst.Status.Endpoint.Address, port)
	if err != nil {
		// Non-fatal — DB works without monitoring
		log.FromContext(ctx).Error(err, "monitoring setup failed (non-fatal)")
		inst.Status.Message = "Available (monitoring setup failed, will retry)"
	} else {
		inst.Status.Resources.ServiceMonitor = smName
		inst.Status.GrafanaURL = grafanaURL
		inst.Status.PrometheusTarget = promTarget
	}

	inst.Status.ProvisioningPhase = dbaasv1.PhaseMonitoringDeployed
	return r.advance(ctx, inst)
}

func (r *DBInstanceReconciler) phaseAvailable(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	inst.Status.Phase = "available"
	inst.Status.ProvisioningPhase = dbaasv1.PhaseAvailable
	inst.Status.ObservedGeneration = inst.Generation
	inst.Status.Message = "Database instance is available"

	return ctrl.Result{}, r.statusUpdate(ctx, inst)
}

// ============================================================
// Stop / Start / Modify / Delete
// ============================================================

func (r *DBInstanceReconciler) reconcileStop(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	ns := inst.Status.Resources.Namespace
	inst.Status.Phase = "stopping"
	inst.Status.Message = "Stopping VM"
	_ = r.statusUpdate(ctx, inst)

	if err := r.Harvester.StopVM(ctx, ns, inst.Status.Resources.VMName); err != nil {
		return r.fail(ctx, inst, "StopFailed", err)
	}

	inst.Status.Phase = "stopped"
	inst.Status.Message = "Stopped. Storage preserved."
	inst.Status.ObservedGeneration = inst.Generation
	return ctrl.Result{}, r.statusUpdate(ctx, inst)
}

func (r *DBInstanceReconciler) reconcileStart(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	ns := inst.Status.Resources.Namespace
	inst.Status.Phase = "starting"
	_ = r.statusUpdate(ctx, inst)

	if err := r.Harvester.StartVM(ctx, ns, inst.Status.Resources.VMName); err != nil {
		return r.fail(ctx, inst, "StartFailed", err)
	}

	inst.Status.Phase = "available"
	inst.Status.Message = "Started"
	inst.Status.ObservedGeneration = inst.Generation
	return ctrl.Result{}, r.statusUpdate(ctx, inst)
}

func (r *DBInstanceReconciler) reconcileModify(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	ns := inst.Status.Resources.Namespace
	inst.Status.Phase = "modifying"
	_ = r.statusUpdate(ctx, inst)

	classSpec := dbaasv1.InstanceClasses[inst.Spec.DBInstanceClass]
	_ = r.Harvester.ResizeVM(ctx, ns, inst.Status.Resources.VMName, classSpec.CPUCores, classSpec.MemoryMB)
	_ = r.Harvester.ResizeDataVolume(ctx, ns, inst.Status.Resources.DataVolumeName, inst.Spec.AllocatedStorage)

	inst.Status.Phase = "available"
	inst.Status.Message = "Modifications applied"
	inst.Status.ObservedGeneration = inst.Generation
	return ctrl.Result{}, r.statusUpdate(ctx, inst)
}

func (r *DBInstanceReconciler) reconcileDelete(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ns := inst.Status.Resources.Namespace

	if inst.Spec.DeletionProtection {
		inst.Status.Message = "Cannot delete: DeletionProtection is enabled"
		_ = r.statusUpdate(ctx, inst)
		return ctrl.Result{}, fmt.Errorf("deletion protection enabled")
	}

	inst.Status.Phase = "deleting"
	inst.Status.Message = "Tearing down resources"
	_ = r.statusUpdate(ctx, inst)

	// Teardown in reverse order — each is safe to retry
	if ns != "" {
		logger.Info("Deleting resources", "namespace", ns)
		r.Harvester.TeardownAll(ctx, inst.Name, ns, inst.Status.Resources)
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(inst, dbaasv1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, inst)
}

// ============================================================
// Helpers
// ============================================================

func (r *DBInstanceReconciler) advance(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	return ctrl.Result{Requeue: true}, r.statusUpdate(ctx, inst)
}

func (r *DBInstanceReconciler) fail(ctx context.Context, inst *dbaasv1.DBInstance, reason string, err error) (ctrl.Result, error) {
	inst.Status.Phase = "failed"
	inst.Status.ProvisioningPhase = dbaasv1.PhaseFailed
	inst.Status.Message = fmt.Sprintf("%s: %v", reason, err)
	_ = r.statusUpdate(ctx, inst)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, err
}

func (r *DBInstanceReconciler) statusUpdate(ctx context.Context, inst *dbaasv1.DBInstance) error {
	return r.Status().Update(ctx, inst)
}
