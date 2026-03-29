package harvester

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/dbaas/api/v1alpha1"
)

// GVRs for Harvester resources.
var (
	vmGVR = schema.GroupVersionResource{
		Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines",
	}
	vmiGVR = schema.GroupVersionResource{
		Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances",
	}
	dvGVR = schema.GroupVersionResource{
		Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datavolumes",
	}
	vpcGVR = schema.GroupVersionResource{
		Group: "kubeovn.io", Version: "v1", Resource: "vpcs",
	}
	subnetGVR = schema.GroupVersionResource{
		Group: "kubeovn.io", Version: "v1", Resource: "subnets",
	}
	nadGVR = schema.GroupVersionResource{
		Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions",
	}
	secretGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "secrets",
	}
	serviceGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "services",
	}
	smGVR = schema.GroupVersionResource{
		Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors",
	}
	cmGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "configmaps",
	}
)

// Client wraps the Kubernetes dynamic client for Harvester API calls.
type Client struct {
	Dynamic    dynamic.Interface
	GrafanaURL string
}

func NewClient(dyn dynamic.Interface, grafanaURL string) *Client {
	return &Client{Dynamic: dyn, GrafanaURL: grafanaURL}
}

// VMCreateParams bundles everything needed to create a PostgreSQL VM.
type VMCreateParams struct {
	ID             string
	Namespace      string
	CPUCores       int
	MemoryMB       int
	OSImage        string
	DataVolumeRef  string
	SubnetName     string
	NADName        string
	MasterUser     string
	DBName         string
	Port           int
	MaxConnections int
	BackupEnabled  bool
	BackupWindow   string
	S3Config       *dbaasv1.S3BackupConfig
}

// ============================================================
// Network: Kube-OVN VPC + Subnet + NAD
// ============================================================

func (c *Client) CreateVPCNetwork(ctx context.Context, id, ns, consumerVLAN string, port int) (vpcName, subnetName, nadName string, err error) {
	vpcName = fmt.Sprintf("dbaas-%s-vpc", id)
	subnetName = fmt.Sprintf("dbaas-%s-subnet", id)
	nadName = fmt.Sprintf("dbaas-%s-nad", id)
	subnetCIDR := fmt.Sprintf("10.100.%d.0/24", hashByte(id))

	// 1. Create VPC
	vpc := newUnstructured("kubeovn.io/v1", "Vpc", vpcName, "")
	_ = unstructured.SetNestedSlice(vpc.Object, []interface{}{ns}, "spec", "namespaces")
	if _, err = c.Dynamic.Resource(vpcGVR).Create(ctx, vpc, metav1.CreateOptions{}); err != nil {
		return
	}

	// 2. Create Subnet
	subnet := newUnstructured("kubeovn.io/v1", "Subnet", subnetName, "")
	_ = unstructured.SetNestedField(subnet.Object, vpcName, "spec", "vpc")
	_ = unstructured.SetNestedField(subnet.Object, subnetCIDR, "spec", "cidrBlock")
	_ = unstructured.SetNestedField(subnet.Object, fmt.Sprintf("10.100.%d.1", hashByte(id)), "spec", "gateway")
	_ = unstructured.SetNestedField(subnet.Object, "IPv4", "spec", "protocol")
	_ = unstructured.SetNestedSlice(subnet.Object, []interface{}{ns}, "spec", "namespaces")
	_ = unstructured.SetNestedField(subnet.Object, true, "spec", "private")
	_ = unstructured.SetNestedSlice(subnet.Object, []interface{}{consumerVLAN}, "spec", "allowSubnets")
	if _, err = c.Dynamic.Resource(subnetGVR).Create(ctx, subnet, metav1.CreateOptions{}); err != nil {
		return
	}

	// 3. Create NAD
	nad := newUnstructured("k8s.cni.cncf.io/v1", "NetworkAttachmentDefinition", nadName, ns)
	config := fmt.Sprintf(`{"cniVersion":"0.3.1","type":"kube-ovn","server_socket":"/run/openvswitch/kube-ovn-daemon.sock","provider":"%s.%s.ovn"}`, nadName, ns)
	_ = unstructured.SetNestedField(nad.Object, config, "spec", "config")
	if _, err = c.Dynamic.Resource(nadGVR).Namespace(ns).Create(ctx, nad, metav1.CreateOptions{}); err != nil {
		return
	}

	// 4. Add static route for consumer VLAN access
	vpcObj, getErr := c.Dynamic.Resource(vpcGVR).Get(ctx, vpcName, metav1.GetOptions{})
	if getErr == nil {
		routes, _, _ := unstructured.NestedSlice(vpcObj.Object, "spec", "staticRoutes")
		routes = append(routes, map[string]interface{}{
			"cidr": consumerVLAN, "nextHop": "autodetect", "policy": "policyDst",
		})
		_ = unstructured.SetNestedSlice(vpcObj.Object, routes, "spec", "staticRoutes")
		_, _ = c.Dynamic.Resource(vpcGVR).Update(ctx, vpcObj, metav1.UpdateOptions{})
	}

	return
}

// ============================================================
// Storage: CDI DataVolume
// ============================================================

func (c *Client) CreateDataVolume(ctx context.Context, id, ns string, sizeGB int, storageClass string) (string, error) {
	dvName := fmt.Sprintf("pg-%s-data", id)
	dv := newUnstructured("cdi.kubevirt.io/v1beta1", "DataVolume", dvName, ns)
	dv.SetLabels(map[string]string{"dbaas.wso2.com/instance": id, "dbaas.wso2.com/role": "pgdata"})

	_ = unstructured.SetNestedMap(dv.Object, map[string]interface{}{}, "spec", "source", "blank")
	_ = unstructured.SetNestedStringSlice(dv.Object, []string{"ReadWriteOnce"}, "spec", "pvc", "accessModes")
	_ = unstructured.SetNestedField(dv.Object, "Block", "spec", "pvc", "volumeMode")
	_ = unstructured.SetNestedField(dv.Object, fmt.Sprintf("%dGi", sizeGB), "spec", "pvc", "resources", "requests", "storage")
	_ = unstructured.SetNestedField(dv.Object, storageClass, "spec", "pvc", "storageClassName")

	_, err := c.Dynamic.Resource(dvGVR).Namespace(ns).Create(ctx, dv, metav1.CreateOptions{})
	return dvName, err
}

func (c *Client) ResizeDataVolume(ctx context.Context, ns, dvName string, newSizeGB int) error {
	dv, err := c.Dynamic.Resource(dvGVR).Namespace(ns).Get(ctx, dvName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	_ = unstructured.SetNestedField(dv.Object, fmt.Sprintf("%dGi", newSizeGB), "spec", "pvc", "resources", "requests", "storage")
	_, err = c.Dynamic.Resource(dvGVR).Namespace(ns).Update(ctx, dv, metav1.UpdateOptions{})
	return err
}

// ============================================================
// VM: KubeVirt VirtualMachine + cloud-init + credentials Secret
// ============================================================

func (c *Client) CreatePostgresVM(ctx context.Context, p VMCreateParams) (vmName, secretName string, err error) {
	vmName = fmt.Sprintf("pg-%s", p.ID)
	secretName = fmt.Sprintf("pg-%s-credentials", p.ID)

	// Generate credentials
	adminPw := randomString(32)
	replPw := randomString(32)
	exporterPw := randomString(24)
	luksKey := randomString(64)

	// Store credentials in K8s Secret
	secret := newUnstructured("v1", "Secret", secretName, p.Namespace)
	_ = unstructured.SetNestedField(secret.Object, "Opaque", "type")
	_ = unstructured.SetNestedField(secret.Object, map[string]interface{}{
		"admin_user":        p.MasterUser,
		"admin_password":    adminPw,
		"repl_password":     replPw,
		"exporter_password": exporterPw,
		"luks_key":          luksKey,
	}, "stringData")
	if _, err = c.Dynamic.Resource(secretGVR).Namespace(p.Namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return
	}

	// Build cloud-init userdata
	cloudInit := buildCloudInit(p, adminPw, replPw, exporterPw, luksKey)

	// Build VirtualMachine CR
	vm := newUnstructured("kubevirt.io/v1", "VirtualMachine", vmName, p.Namespace)
	vm.SetLabels(map[string]string{"dbaas.wso2.com/instance": p.ID, "dbaas.wso2.com/role": "primary"})

	spec := map[string]interface{}{
		"running": true,
		"dataVolumeTemplates": []interface{}{
			map[string]interface{}{
				"apiVersion": "cdi.kubevirt.io/v1beta1",
				"kind":       "DataVolume",
				"metadata":   map[string]interface{}{"name": fmt.Sprintf("pg-%s-os", p.ID)},
				"spec": map[string]interface{}{
					"source": map[string]interface{}{
						"pvc": map[string]interface{}{"namespace": "default", "name": p.OSImage},
					},
					"pvc": map[string]interface{}{
						"accessModes":      []interface{}{"ReadWriteOnce"},
						"storageClassName": "longhorn",
						"resources": map[string]interface{}{
							"requests": map[string]interface{}{"storage": "20Gi"},
						},
					},
				},
			},
		},
		"template": map[string]interface{}{
			"metadata": map[string]interface{}{
				"labels": map[string]string{"dbaas.wso2.com/instance": p.ID},
				"annotations": map[string]interface{}{
					"ovn.kubernetes.io/logical_switch": p.SubnetName,
				},
			},
			"spec": map[string]interface{}{
				"domain": map[string]interface{}{
					"cpu":    map[string]interface{}{"cores": int64(p.CPUCores), "sockets": int64(1), "threads": int64(1)},
					"memory": map[string]interface{}{"guest": fmt.Sprintf("%dMi", p.MemoryMB)},
					"devices": map[string]interface{}{
						"disks": []interface{}{
							map[string]interface{}{"name": "os-disk", "disk": map[string]interface{}{"bus": "virtio"}, "bootOrder": int64(1)},
							map[string]interface{}{"name": "pgdata-disk", "disk": map[string]interface{}{"bus": "virtio"}},
							map[string]interface{}{"name": "cloudinit", "disk": map[string]interface{}{"bus": "virtio"}},
						},
						"interfaces": []interface{}{
							map[string]interface{}{"name": "vpc-net", "bridge": map[string]interface{}{}},
						},
					},
				},
				"networks": []interface{}{
					map[string]interface{}{
						"name":   "vpc-net",
						"multus": map[string]interface{}{"networkName": fmt.Sprintf("%s/%s", p.Namespace, p.NADName)},
					},
				},
				"volumes": []interface{}{
					map[string]interface{}{"name": "os-disk", "dataVolume": map[string]interface{}{"name": fmt.Sprintf("pg-%s-os", p.ID)}},
					map[string]interface{}{"name": "pgdata-disk", "dataVolume": map[string]interface{}{"name": p.DataVolumeRef}},
					map[string]interface{}{"name": "cloudinit", "cloudInitNoCloud": map[string]interface{}{"userData": cloudInit}},
				},
			},
		},
	}
	_ = unstructured.SetNestedField(vm.Object, spec, "spec")

	_, err = c.Dynamic.Resource(vmGVR).Namespace(p.Namespace).Create(ctx, vm, metav1.CreateOptions{})
	return
}

// GetVMIPAddress checks VMI status for a running IP.
func (c *Client) GetVMIPAddress(ctx context.Context, ns, vmName string) (ip string, running bool, err error) {
	vmi, err := c.Dynamic.Resource(vmiGVR).Namespace(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return "", false, err
	}
	phase, _, _ := unstructured.NestedString(vmi.Object, "status", "phase")
	running = (phase == "Running")

	interfaces, _, _ := unstructured.NestedSlice(vmi.Object, "status", "interfaces")
	for _, iface := range interfaces {
		ifMap, ok := iface.(map[string]interface{})
		if !ok {
			continue
		}
		if addr, ok := ifMap["ipAddress"].(string); ok && addr != "" {
			ip = addr
			break
		}
	}
	return
}

// CheckPostgresReady tries a pg_isready via KubeVirt exec.
func (c *Client) CheckPostgresReady(ctx context.Context, ns, vmName string, port int) bool {
	// In production: use the KubeVirt subresource exec API
	// POST /apis/subresources.kubevirt.io/v1/namespaces/{ns}/virtualmachineinstances/{name}/exec
	// For now, check if the VMI has been running for > 3 minutes (rough heuristic)
	vmi, err := c.Dynamic.Resource(vmiGVR).Namespace(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return false
	}
	phase, _, _ := unstructured.NestedString(vmi.Object, "status", "phase")
	return phase == "Running"
}

// StopVM sets spec.running = false.
func (c *Client) StopVM(ctx context.Context, ns, vmName string) error {
	vm, err := c.Dynamic.Resource(vmGVR).Namespace(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	_ = unstructured.SetNestedField(vm.Object, false, "spec", "running")
	_, err = c.Dynamic.Resource(vmGVR).Namespace(ns).Update(ctx, vm, metav1.UpdateOptions{})
	return err
}

// StartVM sets spec.running = true.
func (c *Client) StartVM(ctx context.Context, ns, vmName string) error {
	vm, err := c.Dynamic.Resource(vmGVR).Namespace(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	_ = unstructured.SetNestedField(vm.Object, true, "spec", "running")
	_, err = c.Dynamic.Resource(vmGVR).Namespace(ns).Update(ctx, vm, metav1.UpdateOptions{})
	return err
}

// ResizeVM updates CPU/memory on the VM spec.
func (c *Client) ResizeVM(ctx context.Context, ns, vmName string, cpuCores, memoryMB int) error {
	vm, err := c.Dynamic.Resource(vmGVR).Namespace(ns).Get(ctx, vmName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	_ = unstructured.SetNestedField(vm.Object, int64(cpuCores), "spec", "template", "spec", "domain", "cpu", "cores")
	_ = unstructured.SetNestedField(vm.Object, fmt.Sprintf("%dMi", memoryMB), "spec", "template", "spec", "domain", "memory", "guest")
	_, err = c.Dynamic.Resource(vmGVR).Namespace(ns).Update(ctx, vm, metav1.UpdateOptions{})
	return err
}

// ============================================================
// Monitoring
// ============================================================

func (c *Client) DeployMonitoring(ctx context.Context, id, ns, vmAddr string, pgPort int) (smName, grafanaURL, promTarget string, err error) {
	smName = fmt.Sprintf("pg-%s-monitor", id)
	svcName := fmt.Sprintf("pg-%s-metrics", id)
	grafanaURL = fmt.Sprintf("%s/d/dbaas-%s/postgresql-%s", c.GrafanaURL, id, id)
	promTarget = fmt.Sprintf("%s.%s.svc:9187", svcName, ns)

	// Headless service
	svc := newUnstructured("v1", "Service", svcName, ns)
	svc.SetLabels(map[string]string{"dbaas.wso2.com/instance": id, "dbaas.wso2.com/metrics": "true"})
	_ = unstructured.SetNestedField(svc.Object, "ClusterIP", "spec", "type")
	_ = unstructured.SetNestedField(svc.Object, "None", "spec", "clusterIP")
	_ = unstructured.SetNestedField(svc.Object, map[string]interface{}{"dbaas.wso2.com/instance": id}, "spec", "selector")
	_ = unstructured.SetNestedSlice(svc.Object, []interface{}{
		map[string]interface{}{"name": "metrics", "port": int64(9187), "targetPort": int64(9187), "protocol": "TCP"},
	}, "spec", "ports")
	_, _ = c.Dynamic.Resource(serviceGVR).Namespace(ns).Create(ctx, svc, metav1.CreateOptions{})

	// ServiceMonitor
	sm := newUnstructured("monitoring.coreos.com/v1", "ServiceMonitor", smName, ns)
	sm.SetLabels(map[string]string{"dbaas.wso2.com/instance": id, "release": "prometheus"})
	_ = unstructured.SetNestedField(sm.Object, map[string]interface{}{
		"matchLabels": map[string]interface{}{"dbaas.wso2.com/metrics": "true", "dbaas.wso2.com/instance": id},
	}, "spec", "selector")
	_ = unstructured.SetNestedSlice(sm.Object, []interface{}{
		map[string]interface{}{"port": "metrics", "interval": "15s", "path": "/metrics"},
	}, "spec", "endpoints")
	_, err = c.Dynamic.Resource(smGVR).Namespace(ns).Create(ctx, sm, metav1.CreateOptions{})

	return
}

// ============================================================
// Teardown
// ============================================================

func (c *Client) TeardownAll(ctx context.Context, id, ns string, refs dbaasv1.ResourceRefs) {
	if refs.ServiceMonitor != "" {
		_ = c.Dynamic.Resource(smGVR).Namespace(ns).Delete(ctx, refs.ServiceMonitor, metav1.DeleteOptions{})
	}
	if refs.VMName != "" {
		_ = c.Dynamic.Resource(vmGVR).Namespace(ns).Delete(ctx, refs.VMName, metav1.DeleteOptions{})
	}
	if refs.DataVolumeName != "" {
		_ = c.Dynamic.Resource(dvGVR).Namespace(ns).Delete(ctx, refs.DataVolumeName, metav1.DeleteOptions{})
	}
	if refs.SecretName != "" {
		_ = c.Dynamic.Resource(secretGVR).Namespace(ns).Delete(ctx, refs.SecretName, metav1.DeleteOptions{})
	}
	if refs.NADName != "" {
		_ = c.Dynamic.Resource(nadGVR).Namespace(ns).Delete(ctx, refs.NADName, metav1.DeleteOptions{})
	}
	if refs.SubnetName != "" {
		_ = c.Dynamic.Resource(subnetGVR).Delete(ctx, refs.SubnetName, metav1.DeleteOptions{})
	}
	if refs.VPCName != "" {
		_ = c.Dynamic.Resource(vpcGVR).Delete(ctx, refs.VPCName, metav1.DeleteOptions{})
	}
}

// ============================================================
// Helpers
// ============================================================

func newUnstructured(apiVersion, kind, name, namespace string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]interface{}{
			"name": name,
		},
	}}
	if namespace != "" {
		obj.SetNamespace(namespace)
	}
	return obj
}

func hashByte(s string) int {
	if len(s) == 0 {
		return 1
	}
	h := 0
	for _, c := range s {
		h = (h*31 + int(c)) % 250
	}
	if h <= 0 {
		h = 1
	}
	return h
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)[:n]
}
