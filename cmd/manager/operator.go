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
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/spf13/cobra"

	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	runtimeconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	monitoring "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	monclientv1 "github.com/prometheus-operator/prometheus-operator/pkg/client/versioned/typed/monitoring/v1"

	configv1 "github.com/openshift/api/config/v1"
	tlspkg "github.com/openshift/controller-runtime-common/pkg/tls"
	"github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
	"github.com/openshift/file-integrity-operator/pkg/controller/configmap"
	"github.com/openshift/file-integrity-operator/pkg/controller/fileintegrity"
	"github.com/openshift/file-integrity-operator/pkg/controller/metrics"
	"github.com/openshift/file-integrity-operator/pkg/controller/node"
	"github.com/openshift/file-integrity-operator/pkg/controller/status"
	libgocrypto "github.com/openshift/library-go/pkg/crypto"
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
	maxSecretRetries          = 10

	// tlsProfileFetchTimeout bounds each read of the cluster APIServer
	// singleton used to resolve the TLS profile/adherence policy, so a slow
	// or unreachable API server can't block startup (or a poll tick)
	// indefinitely.
	tlsProfileFetchTimeout = 30 * time.Second
	// tlsProfilePollInterval controls how frequently the operator polls the
	// cluster APIServer for TLS profile/adherence policy changes. Such
	// changes are expected to be rare, deliberate cluster-admin actions (see
	// the CMP-4504 epic's own "plan a maintenance window" guidance), so a
	// coarse interval is fine.
	tlsProfilePollInterval = time.Minute

	// tlsProfileFetchMaxRetries bounds how many extra attempts
	// fetchTLSConfigWithRetry makes, after an initial failed fetchTLSConfig
	// call, before giving up and falling back to defaults. Unlike the
	// recurring poll loop, this boot-time read only ever happens once per
	// pod lifetime, so a single bad-luck transient failure here (e.g. the
	// API server being briefly overloaded - which realistically happens,
	// since applying a cluster-wide TLS profile change itself triggers a
	// full control-plane rollout of etcd/kube-apiserver/openshift-apiserver)
	// would otherwise silently and permanently strand this pod on the weak
	// hardcoded defaults until some unrelated future restart.
	tlsProfileFetchMaxRetries = 2
	// tlsProfileFetchRetryInterval is the fixed delay between retries of a
	// failed boot-time TLS profile fetch. Kept short and constant (not
	// exponential) because this blocks manager startup on the critical path
	// of every pod start: with tlsProfileFetchMaxRetries retries, the
	// worst-case latency this adds (in the failure case; the common case of
	// an immediately-successful first attempt adds none) is bounded to
	// tlsProfileFetchMaxRetries*tlsProfileFetchRetryInterval = 4s.
	// ponytail: each retried attempt still independently inherits
	// fetchTLSConfig's own tlsProfileFetchTimeout (30s) - that per-attempt
	// ceiling already existed for the single pre-existing attempt and is
	// unchanged here, so a pathological run where every attempt hangs for
	// the full timeout is not bounded by this constant. If that compounding
	// needs its own tighter bound too, give fetchTLSConfig a
	// shorter/separate timeout for retried attempts.
	tlsProfileFetchRetryInterval = 2 * time.Second
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	utilruntime.Must(configv1.Install(scheme))
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

	// ctx is cancelled either by an OS shutdown signal or by the TLS profile
	// watcher below when the cluster-wide TLS configuration changes, so the
	// manager shuts down gracefully and the pod restarts with the new TLS
	// settings applied.
	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()

	log.Info("Registering Components.")

	// Metrics has no manager dependency, so create and register it before
	// the TLS profile lookup below: that lets fetch/parse failures be
	// recorded as an observable error metric, not just a log line.
	met := metrics.NewControllerMetrics()
	if err := met.Register(); err != nil {
		log.Error(err, "Error registering metrics")
		os.Exit(1)
	}

	// Fetch the cluster-wide TLS profile and adherence policy up front so all
	// TLS servers (webhook, metrics) can be configured accordingly at
	// startup. Any failure - including a Custom profile with an invalid
	// minTLSVersion, which would otherwise panic when applied - falls back
	// to the pre-existing hardcoded defaults instead of blocking startup.
	// fetchTLSConfigWithRetry retries a small, bounded number of times first
	// (see its and tlsProfileFetchMaxRetries's doc comments) so a single
	// transient failure at this exact moment - this only ever runs once per
	// pod lifetime - doesn't silently and permanently strand this pod on
	// the weak defaults for its entire lifetime.
	preStartClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "unable to create client for TLS profile lookup")
		os.Exit(1)
	}
	// initialTLSProfile/initialTLSAdherencePolicy are exactly what the last
	// fetchTLSConfig attempt inside fetchTLSConfigWithRetry read from the
	// cluster (even if invalid) and are what seed the poll loop below:
	// SecurityProfileWatcher.Reconcile recomputes the same raw values on
	// every tick via the same, non-validating GetTLSProfileSpec, so the seed
	// must match that computation or every tick would see a spurious
	// "change" and restart forever. appliedTLSProfile/appliedTLSAdherencePolicy
	// are what's actually applied to the webhook/metrics TLS config, and fall
	// back to safe defaults on any error so an invalid profile can't panic
	// NewTLSConfigFromProfile.
	initialTLSProfile, initialTLSAdherencePolicy, err := fetchTLSConfigWithRetry(
		ctx, preStartClient, tlsProfileFetchMaxRetries, tlsProfileFetchRetryInterval)
	appliedTLSProfile, appliedTLSAdherencePolicy := initialTLSProfile, initialTLSAdherencePolicy
	if err != nil {
		log.Info("could not fetch cluster APIServer TLS profile after retrying, using defaults", "error", err)
		met.IncFileIntegrityError("cluster_tls_profile_fetch_failed")
		appliedTLSProfile = configv1.TLSProfileSpec{
			Ciphers:       tlspkg.DefaultTLSCiphers,
			MinTLSVersion: tlspkg.DefaultMinTLSVersion,
		}
		appliedTLSAdherencePolicy = configv1.TLSAdherencePolicyNoOpinion
	}
	honorClusterTLSProfile := libgocrypto.ShouldHonorClusterTLSProfile(appliedTLSAdherencePolicy)

	disableHTTP2 := func(c *tls.Config) {
		if enableHTTP2 {
			return
		}
		c.NextProtos = []string{"http/1.1"}
	}
	webhookTLSOpts := []func(config *tls.Config){disableHTTP2}
	if honorClusterTLSProfile {
		applyClusterTLSProfile, unsupported := tlspkg.NewTLSConfigFromProfile(appliedTLSProfile)
		if len(unsupported) > 0 {
			log.Info("cluster TLS profile contains ciphers unsupported by Go", "unsupported", unsupported)
		}
		webhookTLSOpts = append(webhookTLSOpts, applyClusterTLSProfile)
		met.SetTLSConfigFn(applyClusterTLSProfile)
	}

	c := cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Cache: c,
		// Operand NetworkPolicies are read by name during reconcile. The
		// operator's namespaced Role grants get/create/update/delete but not
		// list/watch; reading NetworkPolicy through the informer cache would
		// start a list+watch and fail, so read it directly from the API server.
		Client: client.Options{
			Cache: &client.CacheOptions{
				DisableFor: []client.Object{&networkingv1.NetworkPolicy{}},
			},
		},
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: fmt.Sprintf("%s:%d", metricsHost, metricsPort)},
		HealthProbeBindAddress: ":8081",
		WebhookServer:          webhook.NewServer(webhook.Options{Port: 9443, TLSOpts: webhookTLSOpts}),
		LeaderElection:         true,
		LeaderElectionID:       leaderElectionID,
	})
	if err != nil {
		log.Error(err, "unable to create manager")
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

	// Poll for cluster TLS profile/adherence policy changes and trigger a
	// graceful restart to pick them up, per the documented cluster-wide TLS
	// maintenance window expectations. This deliberately uses a plain poll
	// loop (added as a best-effort Runnable) rather than a controller-runtime
	// watch/informer: an error returned by any Runnable added via mgr.Add
	// aborts every other runnable in the manager - including the core
	// FileIntegrity/Node/Status/Configmap controllers and the metrics server
	// - which would be a disproportionate blast radius for what is meant to
	// be a best-effort hardening feature (e.g. if the new apiservers RBAC
	// hasn't propagated yet during an OLM upgrade). fetchTLSConfig errors
	// here are therefore only logged/counted, never returned.
	tlsProfileWatcherRecorder := mgr.GetEventRecorderFor("tlsprofilewatcher")
	if err := mgr.Add(newTLSProfileWatcher(preStartClient, met, tlsProfileWatcherRecorder, cancel,
		initialTLSProfile, initialTLSAdherencePolicy, tlsProfilePollInterval)); err != nil {
		log.Error(err, "unable to add TLS profile watcher")
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

	sigCtx := ctrl.SetupSignalHandler()
	go func() {
		<-sigCtx.Done()
		cancel()
	}()

	log.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		log.Error(err, "Manager exited non-zero")
		os.Exit(1)
	}
}

// fetchTLSConfig reads the cluster APIServer singleton once and derives both
// the TLS profile and the adherence policy from that single read, so the two
// values can never disagree with each other the way they could if fetched
// independently. A Custom profile with an invalid minTLSVersion is treated
// as a fetch error, since applying it as-is would panic library-go's TLS
// version/cipher resolution.
func fetchTLSConfig(ctx context.Context, cl client.Client) (configv1.TLSProfileSpec, configv1.TLSAdherencePolicy, error) {
	fetchCtx, fetchCancel := context.WithTimeout(ctx, tlsProfileFetchTimeout)
	defer fetchCancel()

	apiServer := &configv1.APIServer{}
	if err := cl.Get(fetchCtx, client.ObjectKey{Name: tlspkg.APIServerName}, apiServer); err != nil {
		return configv1.TLSProfileSpec{}, configv1.TLSAdherencePolicyNoOpinion, fmt.Errorf("failed to get APIServer: %w", err)
	}

	profile, err := tlspkg.GetTLSProfileSpec(apiServer.Spec.TLSSecurityProfile)
	if err != nil {
		return configv1.TLSProfileSpec{}, configv1.TLSAdherencePolicyNoOpinion, fmt.Errorf("invalid TLS profile: %w", err)
	}
	if _, err := libgocrypto.TLSVersion(string(profile.MinTLSVersion)); err != nil {
		// Return the raw profile alongside the error, not a zero value:
		// SecurityProfileWatcher.Reconcile calls this same GetTLSProfileSpec
		// without this validation, so it will keep recomputing this exact
		// raw spec from the cluster on every poll tick. A caller that seeds
		// the watcher with anything else (e.g. a substituted default)
		// instead of this raw value would see a mismatch on every tick and
		// restart forever, since fetchTLSConfig hits this same branch again
		// after each restart.
		return profile, apiServer.Spec.TLSAdherence, fmt.Errorf("invalid minTLSVersion %q: %w", profile.MinTLSVersion, err)
	}

	return profile, apiServer.Spec.TLSAdherence, nil
}

// fetchTLSConfigWithRetry wraps fetchTLSConfig with a short, bounded number
// of retries on failure, so a single transient failure (e.g. the API server
// being briefly overloaded right as this boot-time, once-per-pod-lifetime
// read happens) doesn't permanently strand the pod on the hardcoded TLS
// defaults for its entire lifetime just because the one attempt hit bad
// luck. The common case (first attempt succeeds) adds zero latency, and a
// caller that exhausts every attempt gets back exactly the last attempt's
// (profile, policy, error) - including, for a persistently invalid
// Custom profile's minTLSVersion, the same raw profile/policy fetchTLSConfig
// itself would return on that error path - so the fallback-to-defaults and
// poll-watcher-seeding logic at the call site behaves exactly as if retries
// didn't exist, just later.
//
// maxRetries/retryInterval are parameters (rather than always using the
// tlsProfileFetchMaxRetries/tlsProfileFetchRetryInterval constants), the
// same way newTLSProfileWatcher's pollInterval is, purely so tests can use a
// short interval instead of waiting for production durations; production
// code should always pass those constants.
func fetchTLSConfigWithRetry(ctx context.Context, cl client.Client, maxRetries uint64, retryInterval time.Duration) (
	configv1.TLSProfileSpec, configv1.TLSAdherencePolicy, error) {
	type fetchResult struct {
		profile configv1.TLSProfileSpec
		policy  configv1.TLSAdherencePolicy
	}

	res, err := backoff.RetryNotifyWithData(func() (fetchResult, error) {
		profile, policy, err := fetchTLSConfig(ctx, cl)
		return fetchResult{profile: profile, policy: policy}, err
	}, backoff.WithMaxRetries(backoff.NewConstantBackOff(retryInterval), maxRetries),
		func(err error, wait time.Duration) {
			log.Info("transient failure fetching cluster APIServer TLS profile, retrying",
				"error", err, "wait", wait)
		})

	return res.profile, res.policy, err
}

// newTLSProfileWatcher returns a manager.Runnable that periodically checks
// whether the cluster's TLS profile or adherence policy has changed since
// startup, calling cancel to trigger a graceful shutdown (so the pod
// restarts and re-reads the new configuration) when it has.
//
// It reuses tlspkg.SecurityProfileWatcher's diff/callback logic via direct,
// polled Reconcile calls against an uncached client instead of registering
// it as a controller-runtime watch/informer, so there is no cache-sync
// dependency and no blocking startup path.
//
// Its Start must never return a non-nil error: any error returned by a
// Runnable added via mgr.Add aborts every other runnable in the manager
// (see the comment at its call site). This is deliberately load-bearing -
// see TestNewTLSProfileWatcherNeverReturnsError, which fails if this
// invariant is ever broken (e.g. by a future edit that "cleans up" the
// poll error branch into a return statement).
//
// Because failures are never returned, they are also easy to miss: they
// only show up as a log line and an error_total metric bump, neither of
// which anyone is likely to be watching for an intentionally-quiet
// background poller. A persistent failure (e.g. RBAC that never recovers)
// would otherwise mean the operator silently keeps serving a stale TLS
// profile until its next unrelated restart with no visible signal. So, in
// addition to the log/metric, each failed poll also records a Warning
// Event against the cluster APIServer object - consistent with how other
// controllers in this repo surface user-facing failures (see
// createNodeStatusEvent, status_controller.go, PriorityClass in
// fileintegrity_controller.go), all visible via `oc get events`/`oc
// describe`. Repeated identical-reason events against the same object are
// aggregated by the API server into a single Event with an increasing
// count rather than spamming one object per failed poll (see
// client-go's EventAggregatorByReasonFunc).
//
// pollInterval is a parameter (rather than always using the
// tlsProfilePollInterval constant) purely so tests can use a short
// interval instead of waiting a full minute per iteration; production
// code should always pass tlsProfilePollInterval.
//
// Its mutable fields (on the embedded SecurityProfileWatcher) are only
// ever touched from this single goroutine, so no synchronization is
// needed.
func newTLSProfileWatcher(cl client.Client, met *metrics.Metrics, recorder record.EventRecorder, cancel context.CancelFunc,
	initialProfile configv1.TLSProfileSpec, initialPolicy configv1.TLSAdherencePolicy, pollInterval time.Duration) manager.RunnableFunc {
	// Only used as an event reference: names the singleton object each poll
	// reads, so `oc describe apiserver cluster` surfaces poll failures.
	apiServerRef := &configv1.APIServer{ObjectMeta: metav1.ObjectMeta{Name: tlspkg.APIServerName}}
	watcher := &tlspkg.SecurityProfileWatcher{
		Client:                    cl,
		InitialTLSProfileSpec:     initialProfile,
		InitialTLSAdherencePolicy: initialPolicy,
		OnProfileChange: func(_ context.Context, oldProfile, newProfile configv1.TLSProfileSpec) {
			log.Info("cluster TLS profile changed, restarting to apply new configuration",
				"oldMinTLSVersion", oldProfile.MinTLSVersion, "newMinTLSVersion", newProfile.MinTLSVersion)
			cancel()
		},
		OnAdherencePolicyChange: func(_ context.Context, oldPolicy, newPolicy configv1.TLSAdherencePolicy) {
			log.Info("cluster TLS adherence policy changed, restarting to apply new configuration",
				"oldPolicy", oldPolicy, "newPolicy", newPolicy)
			cancel()
		},
	}
	req := ctrl.Request{NamespacedName: client.ObjectKey{Name: tlspkg.APIServerName}}

	return func(ctx context.Context) error {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				pollCtx, pollCancel := context.WithTimeout(ctx, tlsProfileFetchTimeout)
				_, err := watcher.Reconcile(pollCtx, req)
				pollCancel()
				if err != nil {
					log.Info("could not poll cluster TLS profile, will retry", "error", err)
					met.IncFileIntegrityError("cluster_tls_profile_poll_failed")
					recorder.Eventf(apiServerRef, v1.EventTypeWarning, "ClusterTLSProfile",
						"could not poll cluster TLS profile/adherence policy, keeping previous configuration: %s", err)
				}
			}
		}
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

	// Ensure the metrics secrets are available with retry logic to handle delays in secret sync
	if err := ensureMetricsSecretsWithRetry(ctx, kClient, ns); err != nil {
		return nil, err
	}

	return returnService, nil
}

// ensureMetricsSecretsWithRetry attempts to verify metrics secrets exist with retry logic
// to handle delays in secret sync from service-ca and token controllers.
func ensureMetricsSecretsWithRetry(ctx context.Context, kClient kubernetes.Interface, ns string) error {
	err := backoff.Retry(func() error {
		// Check for serving-cert secret (created by service-ca controller)
		if _, err := kClient.CoreV1().Secrets(ns).Get(ctx, "file-integrity-operator-serving-cert", metav1.GetOptions{}); err != nil {
			if kerr.IsNotFound(err) {
				log.Info("Waiting for file-integrity-operator-serving-cert to be created by service-ca controller, retrying...")
				return err
			}
			return backoff.Permanent(err)
		}

		// Check for metrics service account token secret
		if _, err := kClient.CoreV1().Secrets(ns).Get(ctx, operatorMetricsSecretName, metav1.GetOptions{}); err != nil {
			if kerr.IsNotFound(err) {
				// Create the token secret if it doesn't exist
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
				if _, createErr := kClient.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{}); createErr != nil && !kerr.IsAlreadyExists(createErr) {
					return backoff.Permanent(createErr)
				}
				log.Info("Created operator metrics token secret, waiting for it to be populated, retrying...")
				return errors.New("waiting for metrics token secret to be populated")
			}
			return backoff.Permanent(err)
		}

		return nil
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxSecretRetries))

	if err != nil {
		return fmt.Errorf("failed to ensure metrics secrets after %d retries: %v", maxSecretRetries, err)
	}
	return nil
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

	serviceMonitor := common.GenerateServiceMonitor(service)
	configureMetricsEndpoints(serviceMonitor, namespace)
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

func configureMetricsEndpoints(serviceMonitor *monitoring.ServiceMonitor, namespace string) {
	serverName := fmt.Sprintf("metrics.%s.svc", namespace)
	for i := range serviceMonitor.Spec.Endpoints {
		if serviceMonitor.Spec.Endpoints[i].Port == metrics.ControllerMetricsServiceName {
			serviceMonitor.Spec.Endpoints[i].Path = metrics.HandlerPath
			// Use lowercase "https" instead of
			// monitoring.SchemeHTTPS ("HTTPS") because
			// prometheus-operator 0.87.0 relaxed the validation of
			// this field. Even though we can use HTTPS, we need to
			// support running on versions of OpenShift with
			// prometheus-operator that strictly requires "https".
			// When 0.87.0 is the oldest supported version we can
			// consider reverting back to using SchemeHTTPS.
			scheme := monitoring.Scheme("https")
			serviceMonitor.Spec.Endpoints[i].Scheme = &scheme
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
				TLSFilesConfig: monitoring.TLSFilesConfig{
					CAFile: "/etc/prometheus/configmaps/serving-certs-ca-bundle/service-ca.crt",
				},
			}
		}
	}
}
