package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=dbi
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.spec.dbInstanceClass`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint.address`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DBInstance represents a managed PostgreSQL database on Harvester HCI.
type DBInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DBInstanceSpec   `json:"spec,omitempty"`
	Status DBInstanceStatus `json:"status,omitempty"`
}

// DBInstanceSpec defines the desired state.
type DBInstanceSpec struct {
	// DBInstanceClass maps to CPU/RAM. e.g. "db.t3.medium", "db.m5.large"
	DBInstanceClass string `json:"dbInstanceClass"`

	// EngineVersion is the PostgreSQL major version. Default "16".
	EngineVersion string `json:"engineVersion,omitempty"`

	// DBName is the initial database to create.
	DBName string `json:"dbName,omitempty"`

	// Port for PostgreSQL. Default 5432.
	Port int `json:"port,omitempty"`

	// MasterUsername for the admin user. Default "dbadmin".
	MasterUsername string `json:"masterUsername,omitempty"`

	// ManageMasterUserPassword: if true, auto-generate and store in K8s Secret.
	// If false, read from MasterUserPasswordRef.
	ManageMasterUserPassword bool `json:"manageMasterUserPassword,omitempty"`

	// MasterUserPasswordRef points to a K8s Secret containing the user-supplied password.
	MasterUserPasswordRef *SecretKeyRef `json:"masterUserPasswordRef,omitempty"`

	// AllocatedStorage in GiB.
	AllocatedStorage int `json:"allocatedStorage"`

	// StorageType maps to a Longhorn StorageClass. Default "longhorn".
	StorageType string `json:"storageType,omitempty"`

	// DBSubnetGroupName is the consumer VLAN CIDR or Kube-OVN subnet for app access.
	DBSubnetGroupName string `json:"dbSubnetGroupName,omitempty"`

	// VpcPeering configures Kube-OVN VPC peering between the DBaaS VPC and
	// an external VPC (e.g. the RKE2 cluster VPC) so application pods can
	// reach the database without being co-located in the DBaaS namespace.
	VpcPeering *VpcPeeringConfig `json:"vpcPeering,omitempty"`

	// BackupRetentionPeriod in days. 0 = disabled. Default 7.
	BackupRetentionPeriod int `json:"backupRetentionPeriod,omitempty"`

	// PreferredBackupWindow in UTC. e.g. "02:00-03:00".
	PreferredBackupWindow string `json:"preferredBackupWindow,omitempty"`

	// MultiAZ enables Patroni HA with a standby VM.
	MultiAZ bool `json:"multiAZ,omitempty"`

	// DBParameterGroupRef references a DBParameterGroup by name.
	DBParameterGroupRef string `json:"dbParameterGroupRef,omitempty"`

	// DeletionProtection prevents accidental deletion.
	DeletionProtection bool `json:"deletionProtection,omitempty"`

	// Running controls the VM power state. false = stopped (storage preserved).
	// +kubebuilder:default=true
	Running *bool `json:"running,omitempty"`

	// OSImage is the Harvester VM image name. Default "ubuntu-22.04-server-cloudimg-amd64.img".
	OSImage string `json:"osImage,omitempty"`

	// ConsumerNetwork is the Harvester NAD reference (namespace/name) for a consumer
	// VLAN that application workloads use to reach the database directly via L2.
	// When set, the VM gets a third NIC bridged to this network.
	// Example: "default/vm-net-100"
	ConsumerNetwork string `json:"consumerNetwork,omitempty"`

	// VMPassword sets the default console/SSH password for the VM user (ubuntu).
	// For development and debugging only — leave empty in production.
	VMPassword string `json:"vmPassword,omitempty"`

	// S3BackupConfig for pgBackRest S3 target.
	S3BackupConfig *S3BackupConfig `json:"s3BackupConfig,omitempty"`

	// Tags are user-defined labels.
	Tags map[string]string `json:"tags,omitempty"`
}

type SecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// VpcPeeringConfig specifies the remote VPC and subnet to peer with.
type VpcPeeringConfig struct {
	// RemoteVpc is the name of the Kube-OVN VPC to peer with (e.g. the RKE2 cluster VPC).
	RemoteVpc string `json:"remoteVpc"`

	// RemoteSubnet is the Kube-OVN subnet name in the remote VPC.
	RemoteSubnet string `json:"remoteSubnet"`
}

type S3BackupConfig struct {
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region,omitempty"`
	SecretRef string `json:"secretRef"` // K8s Secret name with accessKey + secretKey
}

// DBInstanceStatus defines the observed state.
type DBInstanceStatus struct {
	// Phase matches RDS DBInstanceStatus strings.
	Phase string `json:"phase,omitempty"`

	// ProvisioningPhase tracks which reconcile step we're on.
	ProvisioningPhase string `json:"provisioningPhase,omitempty"`

	// Conditions for each sub-resource.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Endpoint is populated when the database is reachable.
	Endpoint *Endpoint `json:"endpoint,omitempty"`

	// MasterUserSecret references the K8s Secret with credentials.
	MasterUserSecret *MasterUserSecretRef `json:"masterUserSecret,omitempty"`

	// Resources tracks every Harvester object created for cleanup and idempotency.
	Resources ResourceRefs `json:"resources,omitempty"`

	// Monitoring endpoints.
	GrafanaURL       string `json:"grafanaUrl,omitempty"`
	PrometheusTarget string `json:"prometheusTarget,omitempty"`

	// CACertPEM is the generated CA for SSL verification.
	CACertPEM string `json:"caCertPem,omitempty"`

	// ReadReplicas tracks child replica identifiers.
	ReadReplicas []string `json:"readReplicas,omitempty"`

	// Message is a human-readable description of the current state.
	Message string `json:"message,omitempty"`

	// ObservedGeneration tracks which spec version has been reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

type Endpoint struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	JDBCURL string `json:"jdbcUrl,omitempty"`
}

type MasterUserSecretRef struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "active" or "impaired"
}

// ResourceRefs tracks every Harvester resource the controller created.
// Each field is populated as the corresponding phase completes.
// On controller restart, the reconciler reads these to skip completed phases.
type ResourceRefs struct {
	Namespace      string `json:"namespace,omitempty"`
	VPCName        string `json:"vpcName,omitempty"`
	SubnetName     string `json:"subnetName,omitempty"`
	NADName        string `json:"nadName,omitempty"`
	DataVolumeName string `json:"dataVolumeName,omitempty"`
	VMName         string `json:"vmName,omitempty"`
	SecretName     string `json:"secretName,omitempty"`
	ServiceMonitor string `json:"serviceMonitor,omitempty"`
	VpcPeeringName string `json:"vpcPeeringName,omitempty"`
}

// +kubebuilder:object:root=true
type DBInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DBInstance `json:"items"`
}

// ============================================================
// DBSnapshot
// ============================================================

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=dbs
type DBSnapshot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DBSnapshotSpec   `json:"spec,omitempty"`
	Status DBSnapshotStatus `json:"status,omitempty"`
}

type DBSnapshotSpec struct {
	DBInstanceRef string `json:"dbInstanceRef"`
	SnapshotType  string `json:"snapshotType,omitempty"` // full, diff, incr
}

type DBSnapshotStatus struct {
	Phase   string       `json:"phase,omitempty"` // creating, available, failed
	S3Path  string       `json:"s3Path,omitempty"`
	Size    int64        `json:"size,omitempty"`
	Done    *metav1.Time `json:"done,omitempty"`
	Message string       `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
type DBSnapshotList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DBSnapshot `json:"items"`
}

// ============================================================
// DBParameterGroup
// ============================================================

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=dbpg
type DBParameterGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec DBParameterGroupSpec `json:"spec,omitempty"`
}

type DBParameterGroupSpec struct {
	Family      string            `json:"family"`
	Description string            `json:"description,omitempty"`
	Parameters  map[string]string `json:"parameters"`
}

// +kubebuilder:object:root=true
type DBParameterGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DBParameterGroup `json:"items"`
}

// ============================================================
// Constants
// ============================================================

const (
	PhasePending             = "Pending"
	PhaseNamespaceCreated    = "NamespaceCreated"
	PhaseNetworkProvisioned  = "NetworkProvisioned"
	PhaseStorageProvisioned  = "StorageProvisioned"
	PhaseVMCreated           = "VMCreated"
	PhaseWaitingForCloudInit = "WaitingForCloudInit"
	PhaseDatabaseReady       = "DatabaseReady"
	PhaseMonitoringDeployed  = "MonitoringDeployed"
	PhaseVpcPeeringCreated   = "VpcPeeringCreated"
	PhaseAvailable           = "Available"
	PhaseFailed              = "Failed"

	// Status.Phase values (RDS-compatible lowercase strings)
	StatusCreating  = "creating"
	StatusAvailable = "available"
	StatusStopping  = "stopping"
	StatusStopped   = "stopped"
	StatusStarting  = "starting"
	StatusModifying = "modifying"
	StatusDeleting  = "deleting"
	StatusFailed    = "failed"

	// MasterUserSecretRef.Status values
	SecretStatusActive   = "active"
	SecretStatusImpaired = "impaired"

	// Label keys applied to all Harvester resources owned by a DBInstance
	LabelInstance = "dbaas.wso2.com/instance"
	LabelRole     = "dbaas.wso2.com/role"
	LabelMetrics  = "dbaas.wso2.com/metrics"

	FinalizerName = "dbaas.wso2.com/cleanup"
)

// InstanceClassSpec maps RDS-style class names to Harvester VM resources.
type InstanceClassSpec struct {
	CPUCores       int
	MemoryMB       int
	MaxConnections int
}

var InstanceClasses = map[string]InstanceClassSpec{
	"db.t3.micro":   {1, 1024, 50},
	"db.t3.small":   {1, 2048, 100},
	"db.t3.medium":  {2, 4096, 150},
	"db.t3.large":   {2, 8192, 200},
	"db.t3.xlarge":  {4, 16384, 300},
	"db.m5.large":   {2, 8192, 200},
	"db.m5.xlarge":  {4, 16384, 400},
	"db.m5.2xlarge": {8, 32768, 600},
	"db.m5.4xlarge": {16, 65536, 1000},
	"db.r5.large":   {2, 16384, 300},
	"db.r5.xlarge":  {4, 32768, 500},
	"db.r5.2xlarge": {8, 65536, 800},
}
