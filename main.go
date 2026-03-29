package main

import (
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/dbaas/internal/controller"
	"github.com/wso2/open-cloud-datacenter/dbaas/internal/gateway"
	"github.com/wso2/open-cloud-datacenter/dbaas/internal/harvester"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(dbaasv1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr = flag.String("metrics-bind-address", ":8081", "metrics endpoint")
		probeAddr   = flag.String("health-probe-bind-address", ":8082", "health probe endpoint")
		gatewayAddr = flag.String("gateway-address", ":8080", "REST API gateway address")
		grafanaURL  = flag.String("grafana-url", "https://grafana.monitoring.svc", "Grafana base URL")
	)
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("setup")

	// Build controller manager
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     *metricsAddr,
		HealthProbeBindAddress: *probeAddr,
		LeaderElection:         true,
		LeaderElectionID:       "dbaas-controller.wso2.com",
	})
	if err != nil {
		logger.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// Build dynamic client for Harvester APIs (KubeVirt, CDI, Kube-OVN)
	config := ctrl.GetConfigOrDie()
	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		logger.Error(err, "unable to create dynamic client")
		os.Exit(1)
	}

	hvClient := harvester.NewClient(dynClient, *grafanaURL)

	// Register the DBInstance reconciler
	if err := (&controller.DBInstanceReconciler{
		Client:    mgr.GetClient(),
		Harvester: hvClient,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create controller", "controller", "DBInstance")
		os.Exit(1)
	}

	// Health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// Start REST API gateway in a goroutine
	go func() {
		// Wait for the cache to sync before serving API requests
		if !mgr.GetCache().WaitForCacheSync(ctrl.SetupSignalHandler()) {
			logger.Error(fmt.Errorf("cache sync failed"), "gateway startup aborted")
			return
		}
		if err := gateway.RunGateway(*gatewayAddr, mgr.GetClient()); err != nil {
			logger.Error(err, "gateway failed")
		}
	}()

	// Start controller manager (blocking)
	logger.Info("starting manager",
		"gateway", *gatewayAddr,
		"metrics", *metricsAddr,
		"grafana", *grafanaURL,
	)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "manager exited with error")
		os.Exit(1)
	}
}
