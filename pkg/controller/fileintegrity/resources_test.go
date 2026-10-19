package fileintegrity

import (
	"testing"

	v1alpha1 "github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestGetDaemonResourcesDefaults(t *testing.T) {
	fi := &v1alpha1.FileIntegrity{}

	got := getDaemonResources(fi)

	cases := []struct {
		name     string
		actual   resource.Quantity
		expected resource.Quantity
	}{
		{"memory request", got.Requests[corev1.ResourceMemory], resource.MustParse(common.DefaultDaemonMemoryRequest)},
		{"cpu request", got.Requests[corev1.ResourceCPU], resource.MustParse(common.DefaultDaemonCPURequest)},
		{"memory limit", got.Limits[corev1.ResourceMemory], resource.MustParse(common.DefaultDaemonMemoryLimit)},
		{"cpu limit", got.Limits[corev1.ResourceCPU], resource.MustParse(common.DefaultDaemonCPULimit)},
	}

	for _, c := range cases {
		if !c.actual.Equal(c.expected) {
			t.Errorf("%s mismatch: expected %s, got %s", c.name, c.expected.String(), c.actual.String())
		}
	}
}

func TestGetDaemonResourcesPartial(t *testing.T) {
	q := resource.MustParse
	defReqMem, defReqCPU := q(common.DefaultDaemonMemoryRequest), q(common.DefaultDaemonCPURequest)
	defLimMem, defLimCPU := q(common.DefaultDaemonMemoryLimit), q(common.DefaultDaemonCPULimit)

	cases := []struct {
		name string
		spec corev1.ResourceRequirements
		want corev1.ResourceRequirements
	}{
		{
			name: "only memory limit keeps default requests",
			spec: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: q("2Gi")}},
			want: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: defReqMem, corev1.ResourceCPU: defReqCPU},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: q("2Gi"), corev1.ResourceCPU: defLimCPU},
			},
		},
		{
			name: "only cpu request keeps default limits",
			spec: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: q("100m")}},
			want: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: defReqMem, corev1.ResourceCPU: q("100m")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: defLimMem, corev1.ResourceCPU: defLimCPU},
			},
		},
		{
			name: "limit below default request lowers the request default",
			spec: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: q("20Mi")}},
			want: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: defReqCPU},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: q("20Mi"), corev1.ResourceCPU: defLimCPU},
			},
		},
		{
			name: "request above default limit does not get a conflicting default limit",
			spec: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: q("1Gi")}},
			want: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: q("1Gi"), corev1.ResourceCPU: defReqCPU},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: defLimCPU},
			},
		},
		{
			name: "fully specified values are kept as is",
			spec: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: q("50Mi"), corev1.ResourceCPU: q("50m")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: q("100Mi"), corev1.ResourceCPU: q("100m")},
			},
			want: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: q("50Mi"), corev1.ResourceCPU: q("50m")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: q("100Mi"), corev1.ResourceCPU: q("100m")},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := c.spec
			fi := &v1alpha1.FileIntegrity{Spec: v1alpha1.FileIntegritySpec{Resources: &spec}}
			got := getDaemonResources(fi)
			if !equality.Semantic.DeepEqual(got, c.want) {
				t.Errorf("expected %v, got %v", c.want, got)
			}
			if !equality.Semantic.DeepEqual(spec, c.spec) {
				t.Errorf("spec.resources must not be mutated, got %v", spec)
			}
		})
	}
}
