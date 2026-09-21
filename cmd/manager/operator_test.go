package manager

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/prometheus-operator/prometheus-operator/pkg/client/versioned"
	"github.com/prometheus-operator/prometheus-operator/pkg/client/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	configv1 "github.com/openshift/api/config/v1"
	tlspkg "github.com/openshift/controller-runtime-common/pkg/tls"
	"github.com/openshift/file-integrity-operator/pkg/common"
	"github.com/openshift/file-integrity-operator/pkg/controller/metrics"
)

var _ = Describe("Operator startup tests", func() {
	Context("ServiceMonitor", func() {
		It("Uses lowercase https scheme for compatibility with older CRDs", func() {
			service := &corev1.Service{
				ObjectMeta: v1.ObjectMeta{
					Name:      "test-metrics-service",
					Namespace: "test-ns",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Name: metrics.ControllerMetricsServiceName},
					},
				},
			}
			serviceMonitor := common.GenerateServiceMonitor(service)
			configureMetricsEndpoints(serviceMonitor, "test-ns")
			for _, ep := range serviceMonitor.Spec.Endpoints {
				if ep.Port == metrics.ControllerMetricsServiceName {
					Expect(ep.Scheme).ToNot(BeNil())
					Expect(string(*ep.Scheme)).To(Equal("https"))
				}
			}
		})
	})

	Context("PrometheusRule", func() {
		When("Creating the PrometheusRule", func() {
			var ctx context.Context
			var fakeClient versioned.Interface
			ns := "test-ns"

			BeforeEach(func() {
				ctx = context.Background()
				fakeClient = fake.NewSimpleClientset()
			})

			It("Creates the default Rule if it doesn't exist", func() {
				err := createIntegrityFailureAlert(ctx, fakeClient.MonitoringV1(), ns)
				Expect(err).To(BeNil())
				createdRule, err := fakeClient.MonitoringV1().PrometheusRules(ns).Get(ctx, defaultPrometheusAlertName, v1.GetOptions{})
				Expect(err).To(BeNil())
				Expect(createdRule).To(Not(BeNil()))
				defaultRule := defaultPrometheusRule(defaultPrometheusAlertName, ns)
				Expect(defaultRule).To(Not(BeNil()))
				Expect(createdRule.Spec).To(BeEquivalentTo(defaultRule.Spec))
			})
			It("Updates the default Rule if it exists (and differs)", func() {
				// Created a modified default
				rule := defaultPrometheusRule(defaultPrometheusAlertName, ns)
				rule.Spec.Groups[0].Name = "other-than-default"
				_, err := fakeClient.MonitoringV1().PrometheusRules(ns).Create(ctx, rule, v1.CreateOptions{})
				Expect(err).To(BeNil())
				// Run as normal
				err = createIntegrityFailureAlert(ctx, fakeClient.MonitoringV1(), ns)
				Expect(err).To(BeNil())
				// Verify it changed
				createdRule, err := fakeClient.MonitoringV1().PrometheusRules(ns).Get(ctx, defaultPrometheusAlertName, v1.GetOptions{})
				Expect(err).To(BeNil())
				Expect(createdRule).To(Not(BeNil()))
				defaultRule := defaultPrometheusRule(defaultPrometheusAlertName, ns)
				Expect(defaultRule).To(Not(BeNil()))
				Expect(createdRule.Spec).To(BeEquivalentTo(defaultRule.Spec))
			})
		})
	})

	Context("ensureMetricsSecretsWithRetry", func() {
		var ctx context.Context
		var fakeClient *kubefake.Clientset
		ns := "test-ns"

		BeforeEach(func() {
			ctx = context.Background()
			fakeClient = kubefake.NewSimpleClientset()
		})

		It("Succeeds when both secrets already exist", func() {
			servingCert := &corev1.Secret{
				ObjectMeta: v1.ObjectMeta{
					Name:      "file-integrity-operator-serving-cert",
					Namespace: ns,
				},
				Data: map[string][]byte{
					"tls.crt": []byte("cert-data"),
					"tls.key": []byte("key-data"),
				},
			}
			tokenSecret := &corev1.Secret{
				ObjectMeta: v1.ObjectMeta{
					Name:      operatorMetricsSecretName,
					Namespace: ns,
					Annotations: map[string]string{
						"kubernetes.io/service-account.name": operatorMetricsSA,
					},
				},
				Type: corev1.SecretTypeServiceAccountToken,
				Data: map[string][]byte{
					"token": []byte("test-token"),
				},
			}

			_, err := fakeClient.CoreV1().Secrets(ns).Create(ctx, servingCert, v1.CreateOptions{})
			Expect(err).To(BeNil())
			_, err = fakeClient.CoreV1().Secrets(ns).Create(ctx, tokenSecret, v1.CreateOptions{})
			Expect(err).To(BeNil())

			err = ensureMetricsSecretsWithRetry(ctx, fakeClient, ns)
			Expect(err).To(BeNil())
		})

		It("Creates token secret if it doesn't exist and waits for it to be populated", func() {
			servingCert := &corev1.Secret{
				ObjectMeta: v1.ObjectMeta{
					Name:      "file-integrity-operator-serving-cert",
					Namespace: ns,
				},
			}
			_, err := fakeClient.CoreV1().Secrets(ns).Create(ctx, servingCert, v1.CreateOptions{})
			Expect(err).To(BeNil())

			var attemptCount int32
			fakeClient.PrependReactor("get", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
				getAction := action.(ktesting.GetAction)
				if getAction.GetName() == operatorMetricsSecretName {
					count := atomic.AddInt32(&attemptCount, 1)
					if count <= 2 {
						return true, nil, kerr.NewNotFound(corev1.Resource("secrets"), operatorMetricsSecretName)
					}
					return true, &corev1.Secret{
						ObjectMeta: v1.ObjectMeta{
							Name:      operatorMetricsSecretName,
							Namespace: ns,
							Annotations: map[string]string{
								"kubernetes.io/service-account.name": operatorMetricsSA,
							},
						},
						Type: corev1.SecretTypeServiceAccountToken,
						Data: map[string][]byte{"token": []byte("populated-token")},
					}, nil
				}
				return false, nil, nil
			})

			err = ensureMetricsSecretsWithRetry(ctx, fakeClient, ns)
			Expect(err).To(BeNil())

			secrets, err := fakeClient.CoreV1().Secrets(ns).List(ctx, v1.ListOptions{})
			Expect(err).To(BeNil())
			var tokenSecretFound bool
			for _, secret := range secrets.Items {
				if secret.Name == operatorMetricsSecretName {
					tokenSecretFound = true
					Expect(secret.Annotations["kubernetes.io/service-account.name"]).To(Equal(operatorMetricsSA))
					Expect(secret.Type).To(Equal(corev1.SecretTypeServiceAccountToken))
				}
			}
			Expect(tokenSecretFound).To(BeTrue())
		})

		It("Retries and succeeds when serving cert appears after delay", func() {
			tokenSecret := &corev1.Secret{
				ObjectMeta: v1.ObjectMeta{
					Name:      operatorMetricsSecretName,
					Namespace: ns,
				},
				Type: corev1.SecretTypeServiceAccountToken,
			}
			_, err := fakeClient.CoreV1().Secrets(ns).Create(ctx, tokenSecret, v1.CreateOptions{})
			Expect(err).To(BeNil())

			var attemptCount int32
			fakeClient.PrependReactor("get", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
				getAction := action.(ktesting.GetAction)
				if getAction.GetName() == "file-integrity-operator-serving-cert" {
					count := atomic.AddInt32(&attemptCount, 1)
					if count <= 3 {
						return true, nil, kerr.NewNotFound(corev1.Resource("secrets"), "file-integrity-operator-serving-cert")
					}
					return true, &corev1.Secret{
						ObjectMeta: v1.ObjectMeta{
							Name:      "file-integrity-operator-serving-cert",
							Namespace: ns,
						},
						Data: map[string][]byte{
							"tls.crt": []byte("cert-data"),
						},
					}, nil
				}
				return false, nil, nil
			})

			err = ensureMetricsSecretsWithRetry(ctx, fakeClient, ns)
			Expect(err).To(BeNil())
			Expect(atomic.LoadInt32(&attemptCount)).To(BeNumerically(">", 1))
		})

		It("Fails after max retries when serving cert never appears", func() {
			tokenSecret := &corev1.Secret{
				ObjectMeta: v1.ObjectMeta{
					Name:      operatorMetricsSecretName,
					Namespace: ns,
				},
				Type: corev1.SecretTypeServiceAccountToken,
			}
			_, err := fakeClient.CoreV1().Secrets(ns).Create(ctx, tokenSecret, v1.CreateOptions{})
			Expect(err).To(BeNil())

			err = ensureMetricsSecretsWithRetry(ctx, fakeClient, ns)
			Expect(err).ToNot(BeNil())
			Expect(err.Error()).To(ContainSubstring("failed to ensure metrics secrets after"))
			Expect(err.Error()).To(ContainSubstring("10 retries"))
			Expect(err.Error()).To(ContainSubstring("file-integrity-operator-serving-cert"))
		})

		It("Fails after max retries when token secret is never populated", func() {
			servingCert := &corev1.Secret{
				ObjectMeta: v1.ObjectMeta{
					Name:      "file-integrity-operator-serving-cert",
					Namespace: ns,
				},
			}
			_, err := fakeClient.CoreV1().Secrets(ns).Create(ctx, servingCert, v1.CreateOptions{})
			Expect(err).To(BeNil())

			fakeClient.PrependReactor("get", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
				getAction := action.(ktesting.GetAction)
				if getAction.GetName() == operatorMetricsSecretName {
					return true, nil, kerr.NewNotFound(corev1.Resource("secrets"), operatorMetricsSecretName)
				}
				return false, nil, nil
			})

			err = ensureMetricsSecretsWithRetry(ctx, fakeClient, ns)
			Expect(err).ToNot(BeNil())
			Expect(err.Error()).To(ContainSubstring("failed to ensure metrics secrets after"))
			Expect(err.Error()).To(ContainSubstring("10 retries"))
		})

		It("Stops retrying immediately on permanent errors (non-NotFound)", func() {
			var attemptCount int32
			fakeClient.PrependReactor("get", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
				atomic.AddInt32(&attemptCount, 1)
				getAction := action.(ktesting.GetAction)
				if getAction.GetName() == "file-integrity-operator-serving-cert" {
					return true, nil, kerr.NewForbidden(corev1.Resource("secrets"), "file-integrity-operator-serving-cert", nil)
				}
				return false, nil, nil
			})

			err := ensureMetricsSecretsWithRetry(ctx, fakeClient, ns)
			Expect(err).ToNot(BeNil())
			Expect(err.Error()).To(ContainSubstring("forbidden"))
			Expect(atomic.LoadInt32(&attemptCount)).To(BeNumerically("<=", 2))
		})

		It("Handles race condition where token secret is created by another process", func() {
			servingCert := &corev1.Secret{
				ObjectMeta: v1.ObjectMeta{
					Name:      "file-integrity-operator-serving-cert",
					Namespace: ns,
				},
			}
			_, err := fakeClient.CoreV1().Secrets(ns).Create(ctx, servingCert, v1.CreateOptions{})
			Expect(err).To(BeNil())

			var createAttempt int32
			fakeClient.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
				count := atomic.AddInt32(&createAttempt, 1)
				if count == 1 {
					return true, nil, kerr.NewAlreadyExists(corev1.Resource("secrets"), operatorMetricsSecretName)
				}
				return false, nil, nil
			})

			fakeClient.PrependReactor("get", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
				getAction := action.(ktesting.GetAction)
				if getAction.GetName() == operatorMetricsSecretName && atomic.LoadInt32(&createAttempt) > 0 {
					return true, &corev1.Secret{
						ObjectMeta: v1.ObjectMeta{
							Name:      operatorMetricsSecretName,
							Namespace: ns,
						},
						Type: corev1.SecretTypeServiceAccountToken,
						Data: map[string][]byte{"token": []byte("token-data")},
					}, nil
				}
				return false, nil, nil
			})

			err = ensureMetricsSecretsWithRetry(ctx, fakeClient, ns)
			Expect(err).To(BeNil())
		})
	})

	Context("fetchTLSConfig", func() {
		var ctx context.Context

		BeforeEach(func() {
			ctx = context.Background()
		})

		It("errors when the cluster APIServer object doesn't exist", func() {
			cl := ctrlfake.NewClientBuilder().WithScheme(scheme).Build()
			_, _, err := fetchTLSConfig(ctx, cl)
			Expect(err).To(HaveOccurred())
		})

		It("returns the configured profile and adherence policy from a valid APIServer", func() {
			apiServer := &configv1.APIServer{
				ObjectMeta: v1.ObjectMeta{Name: tlspkg.APIServerName},
				Spec: configv1.APIServerSpec{
					TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileIntermediateType},
					TLSAdherence:       configv1.TLSAdherencePolicyStrictAllComponents,
				},
			}
			cl := ctrlfake.NewClientBuilder().WithScheme(scheme).WithObjects(apiServer).Build()

			profile, policy, err := fetchTLSConfig(ctx, cl)
			Expect(err).ToNot(HaveOccurred())
			Expect(policy).To(Equal(configv1.TLSAdherencePolicyStrictAllComponents))
			Expect(profile).To(Equal(*configv1.TLSProfiles[configv1.TLSProfileIntermediateType]))
		})

		// Regression test: a Custom profile with a malformed minTLSVersion
		// must not reach libgocrypto.TLSVersionOrDie (which panics on any
		// value outside VersionTLS10/11/12/13) - it must surface as a plain
		// error instead, so the caller can fall back to defaults instead of
		// crash-looping the whole operator.
		It("errors instead of panicking on a Custom profile with an invalid minTLSVersion", func() {
			apiServer := &configv1.APIServer{
				ObjectMeta: v1.ObjectMeta{Name: tlspkg.APIServerName},
				Spec: configv1.APIServerSpec{
					TLSSecurityProfile: &configv1.TLSSecurityProfile{
						Type: configv1.TLSProfileCustomType,
						Custom: &configv1.CustomTLSProfile{
							TLSProfileSpec: configv1.TLSProfileSpec{
								MinTLSVersion: "NotARealTLSVersion",
								Ciphers:       []string{"TLS_AES_128_GCM_SHA256"},
							},
						},
					},
				},
			}
			cl := ctrlfake.NewClientBuilder().WithScheme(scheme).WithObjects(apiServer).Build()

			var err error
			Expect(func() {
				_, _, err = fetchTLSConfig(ctx, cl)
			}).ToNot(Panic())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid minTLSVersion"))
		})
	})

	Context("newTLSProfileWatcher", func() {
		// This is a regression test, not just a behavioral one: it exists
		// specifically to fail if a future edit changes the poll loop to
		// propagate a fetch/reconcile error instead of swallowing it, which
		// would silently reintroduce the "any Runnable error kills the
		// whole manager" bug this design fixed. See the doc comment on
		// newTLSProfileWatcher.
		It("never returns an error from Start, even when every poll fails", func() {
			var attempts int32
			// Reconcile treats a NotFound Get as a no-op (not an error), so
			// force a different failure (e.g. as RBAC-not-yet-propagated
			// during an upgrade would) to actually exercise the error path.
			failingClient := interceptor.NewClient(
				ctrlfake.NewClientBuilder().WithScheme(scheme).Build(),
				interceptor.Funcs{
					Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
						atomic.AddInt32(&attempts, 1)
						return errors.New("simulated: RBAC not yet propagated")
					},
				},
			)
			met := metrics.NewControllerMetrics()
			recorder := record.NewFakeRecorder(10)

			runCtx, runCancel := context.WithCancel(context.Background())
			cancelCalled := make(chan struct{})
			watcherCancel := func() { close(cancelCalled) }

			watcher := newTLSProfileWatcher(failingClient, met, recorder, watcherCancel,
				configv1.TLSProfileSpec{}, configv1.TLSAdherencePolicyNoOpinion, 5*time.Millisecond)

			errCh := make(chan error, 1)
			go func() { errCh <- watcher(runCtx) }()

			// Let several poll ticks fail, then stop the watcher the normal
			// way (context cancellation) and confirm it still returns nil.
			Eventually(func() int32 { return atomic.LoadInt32(&attempts) }, time.Second, 5*time.Millisecond).
				Should(BeNumerically(">=", 3))
			runCancel()

			var startErr error
			Eventually(errCh, time.Second).Should(Receive(&startErr))
			Expect(startErr).To(BeNil())

			// The watcher's own cancel callback must never have fired: the
			// simulated failure is a fetch error, not a real profile change.
			Consistently(cancelCalled, 50*time.Millisecond).ShouldNot(BeClosed())

			// A failed poll must still be visible to a cluster admin beyond
			// just the log/metric (see the doc comment on
			// newTLSProfileWatcher): a Warning Event should have fired.
			var recordedEvent string
			Eventually(recorder.Events, time.Second).Should(Receive(&recordedEvent))
			Expect(recordedEvent).To(HavePrefix(corev1.EventTypeWarning + " ClusterTLSProfile "))
			Expect(recordedEvent).To(ContainSubstring("simulated: RBAC not yet propagated"))
		})

		// Regression test: a persistently invalid Custom minTLSVersion must
		// not restart the operator on every single poll tick forever.
		// fetchTLSConfig falls back to safe defaults for what's actually
		// *applied* to the TLS servers, but the watcher must be seeded with
		// the raw (unvalidated) profile/policy fetchTLSConfig also returns -
		// GetTLSProfileSpec, which Reconcile calls on every tick, does not
		// validate minTLSVersion, so it recomputes that same raw spec from
		// the cluster every time. Seeding the watcher with anything else
		// (e.g. the substituted defaults) would make every tick see a
		// mismatch and restart, forever, since fetchTLSConfig hits the same
		// invalid-minTLSVersion branch again after each restart.
		It("does not restart on every poll when a Custom profile has a persistently invalid minTLSVersion", func() {
			apiServer := &configv1.APIServer{
				ObjectMeta: v1.ObjectMeta{Name: tlspkg.APIServerName},
				Spec: configv1.APIServerSpec{
					TLSSecurityProfile: &configv1.TLSSecurityProfile{
						Type: configv1.TLSProfileCustomType,
						Custom: &configv1.CustomTLSProfile{
							TLSProfileSpec: configv1.TLSProfileSpec{
								MinTLSVersion: "NotARealTLSVersion",
								Ciphers:       []string{"TLS_AES_128_GCM_SHA256"},
							},
						},
					},
				},
			}
			cl := ctrlfake.NewClientBuilder().WithScheme(scheme).WithObjects(apiServer).Build()

			// Mirror exactly what RunOperator does: seed the watcher with
			// whatever fetchTLSConfig returns, even on error.
			seedProfile, seedPolicy, err := fetchTLSConfig(context.Background(), cl)
			Expect(err).To(HaveOccurred())

			met := metrics.NewControllerMetrics()
			recorder := record.NewFakeRecorder(10)
			cancelCalled := make(chan struct{})
			watcherCancel := func() { close(cancelCalled) }

			watcher := newTLSProfileWatcher(cl, met, recorder, watcherCancel, seedProfile, seedPolicy, 5*time.Millisecond)

			runCtx, runCancel := context.WithCancel(context.Background())
			errCh := make(chan error, 1)
			go func() { errCh <- watcher(runCtx) }()

			// Let several poll ticks run against the still-invalid, unchanged
			// cluster object, then confirm cancel was never triggered.
			time.Sleep(50 * time.Millisecond)
			runCancel()
			Eventually(errCh, time.Second).Should(Receive(BeNil()))
			Expect(cancelCalled).ToNot(BeClosed())
		})
	})
})
