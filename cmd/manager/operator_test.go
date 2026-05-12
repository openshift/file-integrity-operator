package manager

import (
	"context"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/prometheus-operator/prometheus-operator/pkg/client/versioned"
	"github.com/prometheus-operator/prometheus-operator/pkg/client/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
})
