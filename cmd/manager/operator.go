/*
Copyright © 2019 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package main

import (
	"context"
	"flag"
	"fmt"
	monitoring "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/spf13/cobra"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"os"
	rt "runtime"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"

	monclientv1 "github.com/coreos/prometheus-operator/pkg/client/versioned/typed/monitoring/v1"
	"github.com/operator-framework/operator-sdk/pkg/k8sutil"
	kubemetrics "github.com/operator-framework/operator-sdk/pkg/kube-metrics"
	"github.com/operator-framework/operator-sdk/pkg/leader"
	"github.com/operator-framework/operator-sdk/pkg/log/zap"
	"github.com/operator-framework/operator-sdk/pkg/metrics"
	sdkVersion "github.com/operator-framework/operator-sdk/version"
	"k8s.io/client-go/discovery"
	runtimeconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/openshift/file-integrity-operator/pkg/apis"
	"github.com/openshift/file-integrity-operator/pkg/controller"
	ctrlMetrics "github.com/openshift/file-integrity-operator/pkg/controller/metrics"
)

var operatorCmd = &cobra.Command{
	Use:   "operator",
	Short: "The file-integrity-operator command",
	Long:  `An OpenShift operator that manages file integrity checking on cluster nodes.`,
	Run:   RunOperator,
}

// Change below variables to serve metrics on different host or port.
var (
	metricsHost               = "0.0.0.0"
	metricsPort         int32 = 8383
	operatorMetricsPort int32 = 8686
	alertName                 = "file-integrity"
)
var log = logf.Log.WithName("cmd")

func printVersion() {
	log.Info(fmt.Sprintf("Go Version: %s", rt.Version()))
	log.Info(fmt.Sprintf("Go OS/Arch: %s/%s", rt.GOOS, rt.GOARCH))
	log.Info(fmt.Sprintf("Version of operator-sdk: %v", sdkVersion.Version))
}

func RunOperator(cmd *cobra.Command, args []string) {
	// Add the zap logger flag set to the CLI. The flag set must
	// be added before calling pflag.Parse().
	flags := cmd.Flags()
	flags.AddFlagSet(zap.FlagSet())

	// Add flags registered by imported packages (e.g. glog and
	// controller-runtime)
	flags.AddGoFlagSet(flag.CommandLine)
	if err := flags.Parse(zap.FlagSet().Args()); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse zap flagset: %v", zap.FlagSet().Args())
		os.Exit(1)
	}

	// Use a zap logr.Logger implementation. If none of the zap
	// flags are configured (or if the zap flag set is not being
	// used), this defaults to a production zap logger.
	//
	// The logger instantiated here can be changed to any logger
	// implementing the logr.Logger interface. This logger will
	// be propagated through the whole operator, generating
	// uniform and structured logs.
	logf.SetLogger(zap.Logger())

	printVersion()

	namespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		log.Error(err, "Failed to get watch namespace")
		os.Exit(1)
	}

	// Get a config to talk to the apiserver
	cfg, err := runtimeconfig.GetConfig()
	if err != nil {
		log.Error(err, "")
		os.Exit(1)
	}

	ctx := context.TODO()
	// Become the leader before proceeding
	err = leader.Become(ctx, "file-integrity-operator-lock")
	if err != nil {
		log.Error(err, "")
		os.Exit(1)
	}

	// Create a new Cmd to provide shared dependencies and start components
	mgr, err := manager.New(cfg, manager.Options{
		Namespace:          namespace,
		MetricsBindAddress: fmt.Sprintf("%s:%d", metricsHost, metricsPort),
	})
	if err != nil {
		log.Error(err, "")
		os.Exit(1)
	}

	log.Info("Registering Components.")

	met := ctrlMetrics.New()
	if err := met.Register(); err != nil {
		log.Error(err, "Error registering metrics")
		os.Exit(1)
	}

	// Setup Scheme for all resources
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		log.Error(err, "Error adding to scheme")
		os.Exit(1)
	}

	// Setup all Controllers
	if err := controller.AddToManager(mgr, met); err != nil {
		log.Error(err, "Error registering manager")
		os.Exit(1)
	}

	// Serves metrics on the CRDs, separate from
	if err = serveCRMetrics(cfg); err != nil {
		log.Error(err, "Could not generate and serve custom resource metrics")
		os.Exit(1)
	}

	// Add to the below struct any other metrics ports you want to expose.
	servicePorts := []v1.ServicePort{
		{
			Port:       metricsPort,
			Name:       metrics.OperatorPortName,
			Protocol:   v1.ProtocolTCP,
			TargetPort: intstr.IntOrString{Type: intstr.Int, IntVal: metricsPort},
		},
		{
			Port:       operatorMetricsPort,
			Name:       metrics.CRPortName,
			Protocol:   v1.ProtocolTCP,
			TargetPort: intstr.IntOrString{Type: intstr.Int, IntVal: operatorMetricsPort},
		},
		{
			Port:       ctrlMetrics.ControllerMetricsPort,
			Name:       ctrlMetrics.ControllerMetricsServiceName,
			Protocol:   v1.ProtocolTCP,
			TargetPort: intstr.IntOrString{Type: intstr.Int, IntVal: ctrlMetrics.ControllerMetricsPort},
		},
	}

	// Create Service object to expose the metrics port(s).
	service, err := metrics.CreateMetricsService(ctx, cfg, servicePorts)
	if err != nil {
		log.Error(err, "Could not generate metrics service")
		os.Exit(1)
	}

	servMon := metrics.GenerateServiceMonitor(service)
	for i, _ := range servMon.Spec.Endpoints {
		if servMon.Spec.Endpoints[i].Port == ctrlMetrics.ControllerMetricsServiceName {
			servMon.Spec.Endpoints[i].Path = ctrlMetrics.HandlerPath
		}
	}

	serviceMonitorObjectExists, err := k8sutil.ResourceExists(discovery.NewDiscoveryClientForConfigOrDie(cfg),
		"monitoring.coreos.com/v1", "ServiceMonitor")
	if err != nil {
		log.Error(err, "Could not detect service monitor CRD")
		os.Exit(1)
	}

	mclient := monclientv1.NewForConfigOrDie(cfg)
	if serviceMonitorObjectExists {
		_, err = mclient.ServiceMonitors(namespace).Create(ctx, servMon, metav1.CreateOptions{})
		if err != nil && !kerr.IsAlreadyExists(err) {
			log.Error(err, "Could not create service monitor")
			os.Exit(1)
		}
		if kerr.IsAlreadyExists(err) {
			currentServiceMonitor, getErr := mclient.ServiceMonitors(namespace).Get(ctx, servMon.Name, metav1.GetOptions{})
			if getErr != nil {
				log.Error(getErr, "Error fetching service monitor")
				os.Exit(1)
			}
			currentServiceMonitorCopy := currentServiceMonitor.DeepCopy()
			currentServiceMonitorCopy.Spec = servMon.Spec
			_, updateErr := mclient.ServiceMonitors(namespace).Update(ctx, currentServiceMonitorCopy, metav1.UpdateOptions{})
			if updateErr != nil {
				log.Error(updateErr, "Error updating service monitor")
				os.Exit(1)
			}
		}
	} else {
		log.Info("Install prometheus-operator in your cluster to create ServiceMonitor objects")
	}

	rule := monitoring.Rule{
		Alert: "NodeHasIntegrityFailure",
		Expr:  intstr.FromString(`file_integrity_operator_node_failed{node=~".+"} == 1`),
		For:   "1s",
		Labels: map[string]string{
			"severity": "warning",
		},
		Annotations: map[string]string{
			"summary":     "Node {{ $labels.node }} has a file integrity failure",
			"description": "Node {{ $labels.node }} has an integrity check status of Failed for more than 1 second.",
		},
	}
	_, createErr := mclient.PrometheusRules(namespace).Create(ctx, &monitoring.PrometheusRule{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      alertName,
		},
		Spec: monitoring.PrometheusRuleSpec{
			Groups: []monitoring.RuleGroup{
				{
					Name: "node-failed",
					Rules: []monitoring.Rule{
						rule,
					},
				},
			},
		},
	}, metav1.CreateOptions{})
	if createErr != nil && !kerr.IsAlreadyExists(createErr) {
		log.Info("could not create prometheus rule for alert", createErr)
	}

	log.Info("Starting manager")
	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		log.Error(err, "Manager exited non-zero")
		os.Exit(1)
	}
}

// serveCRMetrics gets the Operator/CustomResource GVKs and generates metrics based on those types.
// It serves those metrics on "http://metricsHost:operatorMetricsPort".
func serveCRMetrics(cfg *rest.Config) error {
	// Below function returns filtered operator/CustomResource specific GVKs.
	// For more control override the below GVK list with your own custom logic.
	filteredGVK, err := k8sutil.GetGVKsFromAddToScheme(apis.AddToScheme)
	if err != nil {
		return err
	}
	// Get the namespace the operator is currently deployed in.
	operatorNs, err := k8sutil.GetOperatorNamespace()
	if err != nil {
		return err
	}
	// To generate metrics in other namespaces, add the values below.
	ns := []string{operatorNs}
	// Generate and serve custom resource specific metrics.
	err = kubemetrics.GenerateAndServeCRMetrics(cfg, ns, filteredGVK, metricsHost, operatorMetricsPort)
	if err != nil {
		return err
	}
	return nil
}
