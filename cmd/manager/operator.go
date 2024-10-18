/*
Copyright © 2019 - 2022 Red Hat Inc.

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
package manager

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"
	rt "runtime"

	"github.com/spf13/cobra"

	v1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	runtimeconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	monitoring "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	monclientv1 "github.com/prometheus-operator/prometheus-operator/pkg/client/versioned/typed/monitoring/v1"

	"github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
	"github.com/openshift/file-integrity-operator/pkg/controller/configmap"
	"github.com/openshift/file-integrity-operator/pkg/controller/fileintegrity"
	"github.com/openshift/file-integrity-operator/pkg/controller/metrics"
	"github.com/openshift/file-integrity-operator/pkg/controller/node"
	"github.com/openshift/file-integrity-operator/pkg/controller/status"
)

var OperatorCmd = &cobra.Command{
	Use:   "operator",
	Short: "The file-integrity-operator command",
	Long:  `An OpenShift operator that manages file integrity checking on cluster nodes.`,
	Run:   RunOperator,
}

var (
	scheme = runtime.NewScheme()
	log    = logf.Log.WithName("cmd")
)

const (
	operatorMetricsSA         = "file-integrity-operator-metrics"
	operatorMetricsSecretName = "file-integrity-operator-metrics-token"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

// Change below variables to serve metrics on different host or port.
var (
	metricsHost                      = "0.0.0.0"
	metricsPort                int32 = 8383
	defaultPrometheusAlertName       = "file-integrity"
	metricsServiceName               = "metrics"
	leaderElectionID                 = "962a0cf2.openshift.io"
	enableHTTP2                      = false
)

func printVersion() {
	log.Info(fmt.Sprintf("Go Version: %s", rt.Version()))
	log.Info(fmt.Sprintf("Go OS/Arch: %s/%s", rt.GOOS, rt.GOARCH))
}

func RunOperator(cmd *cobra.Command, args []string) {
	flags := cmd.Flags()
	flags.AddGoFlagSet(flag.CommandLine)
	flags.Parse(args)

	ctrl.SetLogger(zap.New())

	printVersion()

	namespace, err := common.GetWatchNamespace()
	if err != nil {
		log.Error(err, "Failed to get watch namespace")
		os.Exit(1)
	}
	log.Info("using watch namespace", "WatchNamespace", namespace)

	// Get a config to talk to the apiserver
	cfg, err := runtimeconfig.GetConfig()
	if err != nil {
		log.Error(err, "")
		os.Exit(1)
	}

	kubeClient := kubernetes.NewForConfigOrDie(cfg)
	monitoringClient := monclientv1.NewForConfigOrDie(cfg)

	ctx := context.TODO()

	log.Info("Registering Components.")

	disableHTTP2 := func(c *tls.Config) {
		if enableHTTP2 {
			return
		}
		c.NextProtos = []string{"http/1.1"}
	}
	c := cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Cache:                  c,
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: fmt.Sprintf("%s:%d", metricsHost, metricsPort)},
		HealthProbeBindAddress: ":8081",
		WebhookServer:          webhook.NewServer(webhook.Options{Port: 9443, TLSOpts: []func(config *tls.Config){disableHTTP2}}),
		LeaderElection:         true,
		LeaderElectionID:       leaderElectionID,
	})
	if err != nil {
		log.Error(err, "unable to create manager")
		os.Exit(1)
	}

	met := metrics.NewControllerMetrics()
	if err := met.Register(); err != nil {
		log.Error(err, "Error registering metrics")
		os.Exit(1)
	}

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// Setup all Controllers
	if err := fileintegrity.AddFileIntegrityController(mgr, met); err != nil {
		log.Error(err, "Error registering manager with FI controller")
		os.Exit(1)
	}

	if err := node.AddNodeController(mgr, met); err != nil {
		log.Error(err, "Error registering manager with Node controller")
		os.Exit(1)
	}

	if err := status.AddStatusController(mgr, met); err != nil {
		log.Error(err, "Error registering manager with Status controller")
		os.Exit(1)
	}

	if err := configmap.AddConfigmapController(mgr, met); err != nil {
		log.Error(err, "Error registering manager with Configmap controller")
		os.Exit(1)
	}

	// Add metrics controller to manager
	if err := mgr.Add(met); err != nil {
		log.Error(err, "Error registering controller metrics")
		os.Exit(1)
	}

	// Create the metrics service and make sure the service-secret is available
	metricsService, err := ensureMetricsServiceAndSecret(ctx, kubeClient, namespace)
	if err != nil {
		log.Error(err, "Error creating metrics service/secret")
		os.Exit(1)
	}

	if err := createServiceMonitor(ctx, cfg, monitoringClient, kubeClient, namespace, metricsService); err != nil {
		log.Error(err, "Error creating ServiceMonitor")
		os.Exit(1)
	}

	if err := createIntegrityFailureAlert(ctx, monitoringClient, namespace); err != nil {
		log.Error(err, "Error creating alert")
		os.Exit(1)
	}

	log.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "Manager exited non-zero")
		os.Exit(1)
	}
}

func ensureMetricsServiceAndSecret(ctx context.Context, kClient *kubernetes.Clientset, ns string) (*v1.Service, error) {
	var returnService *v1.Service
	var err error

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"name": "file-integrity-operator",
			},
			Annotations: map[string]string{
				"service.beta.openshift.io/serving-cert-secret-name": "file-integrity-operator-serving-cert",
			},
			Name:      metricsServiceName,
			Namespace: ns,
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:       "metrics",
					Port:       metricsPort,
					TargetPort: intstr.FromInt(int(metricsPort)),
					Protocol:   v1.ProtocolTCP,
				},
				{
					Name:       "metrics-fio",
					Port:       metrics.ControllerMetricsPort,
					TargetPort: intstr.FromInt(int(metrics.ControllerMetricsPort)),
					Protocol:   v1.ProtocolTCP,
				},
			},
			Selector: map[string]string{
				"name": "file-integrity-operator",
			},
			Type: v1.ServiceTypeClusterIP,
		},
	}

	createdService, err := kClient.CoreV1().Services(ns).Create(ctx, newService, metav1.CreateOptions{})
	if err != nil && !kerr.IsAlreadyExists(err) {
		return nil, err
	}
	if kerr.IsAlreadyExists(err) {
		curService, getErr := kClient.CoreV1().Services(ns).Get(ctx, metricsServiceName, metav1.GetOptions{})
		if getErr != nil {
			return nil, getErr
		}
		returnService = curService

		// Needs update?
		if !reflect.DeepEqual(curService.Spec, newService.Spec) {
			serviceCopy := curService.DeepCopy()
			serviceCopy.Spec = newService.Spec

			// OCP-4.6 only - Retain ClusterIP from the current service in case we overwrite it when copying the updated
			// service. Avoids "Error creating metrics service/secret","error":"Service \"metrics\" is invalid: spec.clusterIP:
			// Invalid value: \"\": field is immutable","stacktrace"...
			if len(serviceCopy.Spec.ClusterIP) == 0 {
				serviceCopy.Spec.ClusterIP = curService.Spec.ClusterIP
			}

			updatedService, updateErr := kClient.CoreV1().Services(ns).Update(ctx, serviceCopy, metav1.UpdateOptions{})
			if updateErr != nil {
				return nil, updateErr
			}
			returnService = updatedService
		}
	} else {
		returnService = createdService
	}

	// Ensure the serving-cert secret for metrics is available, we have to exit and restart if not
	if _, err := kClient.CoreV1().Secrets(ns).Get(ctx, "file-integrity-operator-serving-cert", metav1.GetOptions{}); err != nil {
		if kerr.IsNotFound(err) {
			return nil, errors.New("file-integrity-operator-serving-cert not found - restarting, as the service may have just been created")
		} else {
			return nil, err
		}
	}

	// Check if the metrics service account token secret exists. If not, create it and trigger a restart.
	_, err = kClient.CoreV1().Secrets(ns).Get(ctx, operatorMetricsSecretName, metav1.GetOptions{})
	if err != nil {
		if kerr.IsNotFound(err) {
			secret := &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      operatorMetricsSecretName,
					Namespace: ns,
					Annotations: map[string]string{
						"kubernetes.io/service-account.name": operatorMetricsSA,
					},
				},
				Type: v1.SecretTypeServiceAccountToken,
			}
			if _, createErr := kClient.CoreV1().Secrets(ns).Create(context.TODO(), secret, metav1.CreateOptions{}); createErr != nil && !kerr.IsAlreadyExists(createErr) {
				return nil, createErr
			}
			return nil, errors.New("operator metrics token not found; restarting as the service may have just been created")
		}
		return nil, err
	}

	return returnService, nil
}

func defaultPrometheusRule(alertName, namespace string) *monitoring.PrometheusRule {
	duration := monitoring.Duration("1s")
	rule := monitoring.Rule{
		Alert: "NodeHasIntegrityFailure",
		Expr:  intstr.FromString(`file_integrity_operator_node_failed{node=~".+"} * on(node) kube_node_info > 0`),
		For:   &duration,
		Labels: map[string]string{
			"severity":  "warning",
			"namespace": namespace,
		},
		Annotations: map[string]string{
			"summary":     "Node {{ $labels.node }} has a file integrity failure",
			"description": "Node {{ $labels.node }} has an integrity check status of Failed for more than 1 second.",
		},
	}

	return &monitoring.PrometheusRule{
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
	}
}

// createIntegrityFailureAlert tries to create or update the default PrometheusRule, returning any errors.
func createIntegrityFailureAlert(ctx context.Context, client monclientv1.MonitoringV1Interface, namespace string) error {
	promRule := defaultPrometheusRule(defaultPrometheusAlertName, namespace)
	_, createErr := client.PrometheusRules(namespace).Create(ctx, promRule, metav1.CreateOptions{})
	if createErr != nil && !kerr.IsAlreadyExists(createErr) {
		return createErr
	}

	if kerr.IsAlreadyExists(createErr) {
		currentPromRule, getErr := client.PrometheusRules(namespace).Get(ctx, promRule.Name,
			metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		if !reflect.DeepEqual(currentPromRule.Spec, promRule.Spec) {
			promRuleCopy := currentPromRule.DeepCopy()
			promRuleCopy.Spec = promRule.Spec
			if _, updateErr := client.PrometheusRules(namespace).Update(ctx, promRuleCopy,
				metav1.UpdateOptions{}); updateErr != nil {
				return updateErr
			}
		}
	}
	return nil
}

// tryCreatingServiceMonitor attempts to create a ServiceMonitor out of service, and updates it to include the controller
// metrics paths.
func createServiceMonitor(ctx context.Context, cfg *rest.Config, mClient *monclientv1.MonitoringV1Client, kubeClient *kubernetes.Clientset,
	namespace string, service *v1.Service) error {
	ok, err := common.ResourceExists(discovery.NewDiscoveryClientForConfigOrDie(cfg),
		"monitoring.coreos.com/v1", "ServiceMonitor")
	if err != nil {
		return err
	}
	if !ok {
		log.Info("Install prometheus-operator in your cluster to create ServiceMonitor objects")
		return nil
	}

	serverName := fmt.Sprintf("metrics.%s.svc", namespace)
	serviceMonitor := common.GenerateServiceMonitor(service)
	for i := range serviceMonitor.Spec.Endpoints {
		if serviceMonitor.Spec.Endpoints[i].Port == metrics.ControllerMetricsServiceName {
			serviceMonitor.Spec.Endpoints[i].Path = metrics.HandlerPath
			serviceMonitor.Spec.Endpoints[i].Scheme = "https"
			serviceMonitor.Spec.Endpoints[i].Authorization = &monitoring.SafeAuthorization{
				Type: "Bearer",
				Credentials: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: operatorMetricsSecretName,
					},
					Key: "token",
				},
			}
			serviceMonitor.Spec.Endpoints[i].TLSConfig = &monitoring.TLSConfig{
				SafeTLSConfig: monitoring.SafeTLSConfig{
					ServerName: &serverName,
				},
				CAFile: "/etc/prometheus/configmaps/serving-certs-ca-bundle/service-ca.crt",
			}
		}
	}
	_, err = mClient.ServiceMonitors(namespace).Create(ctx, serviceMonitor, metav1.CreateOptions{})
	if err != nil && !kerr.IsAlreadyExists(err) {
		return err
	}
	if kerr.IsAlreadyExists(err) {
		currentServiceMonitor, getErr := mClient.ServiceMonitors(namespace).Get(ctx, serviceMonitor.Name,
			metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		serviceMonitorCopy := currentServiceMonitor.DeepCopy()
		serviceMonitorCopy.Spec = serviceMonitor.Spec
		if _, updateErr := mClient.ServiceMonitors(namespace).Update(ctx, serviceMonitorCopy,
			metav1.UpdateOptions{}); updateErr != nil {
			return updateErr
		}
	}
	return nil
}
