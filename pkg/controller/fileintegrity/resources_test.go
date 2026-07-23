package fileintegrity

import (
	"testing"

	v1alpha1 "github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
	corev1 "k8s.io/api/core/v1"
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
