package fileintegrity

import (
	"reflect"
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

func TestResourceRequestLimitConflicts(t *testing.T) {
	q := resource.MustParse

	cases := []struct {
		name string
		res  corev1.ResourceRequirements
		want []string
	}{
		{
			name: "no requests or limits",
			res:  corev1.ResourceRequirements{},
			want: nil,
		},
		{
			name: "request equal to limit",
			res: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: q("600Mi"), corev1.ResourceCPU: q("300m")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: q("600Mi"), corev1.ResourceCPU: q("300m")},
			},
			want: nil,
		},
		{
			name: "request below limit",
			res: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: q("40Mi"), corev1.ResourceCPU: q("40m")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: q("600Mi"), corev1.ResourceCPU: q("300m")},
			},
			want: nil,
		},
		{
			name: "request without a limit is allowed",
			res: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: q("2Gi")},
			},
			want: nil,
		},
		{
			name: "limit without a request is allowed",
			res: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: q("20Mi")},
			},
			want: nil,
		},
		{
			name: "memory request above limit",
			res: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: q("2Gi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: q("1Gi")},
			},
			want: []string{"memory request 2Gi exceeds limit 1Gi"},
		},
		{
			name: "cpu request above limit",
			res: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: q("500m")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: q("300m")},
			},
			want: []string{"cpu request 500m exceeds limit 300m"},
		},
		{
			name: "both resources above limit are reported in name order",
			res: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: q("2Gi"), corev1.ResourceCPU: q("1")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: q("1Gi"), corev1.ResourceCPU: q("500m")},
			},
			want: []string{
				"cpu request 1 exceeds limit 500m",
				"memory request 2Gi exceeds limit 1Gi",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resourceRequestLimitConflicts(c.res)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("expected %v, got %v", c.want, got)
			}
		})
	}
}

// Default filling must never create a conflict on its own: getDaemonResources
// skips a default that would clash with a user value. Any conflict reported for
// the effective requirements must therefore come from the user's own values.
func TestGetDaemonResourcesNeverConflictsByDefaulting(t *testing.T) {
	partial := []corev1.ResourceRequirements{
		{},
		{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("20Mi")}},
		{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")}},
		{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}},
		{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")}},
	}

	for i := range partial {
		spec := partial[i]
		fi := &v1alpha1.FileIntegrity{Spec: v1alpha1.FileIntegritySpec{Resources: &spec}}
		if conflicts := resourceRequestLimitConflicts(getDaemonResources(fi)); len(conflicts) != 0 {
			t.Errorf("case %d: defaulting introduced conflicts %v for spec %v", i, conflicts, spec)
		}
	}
}
