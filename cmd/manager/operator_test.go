package manager

import (
	"context"
	"sync/atomic"

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
})
