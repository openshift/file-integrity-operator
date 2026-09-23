package fileintegrity

import (
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
)

const (
	testFIName = "test-fi"
	testDSName = "aide-test-fi"
)

func testFileIntegrity(labels, annotations map[string]string) *v1alpha1.FileIntegrity {
	return &v1alpha1.FileIntegrity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testFIName,
			Namespace: common.FileIntegrityNamespace,
		},
		Spec: v1alpha1.FileIntegritySpec{
			Labels:      labels,
			Annotations: annotations,
		},
	}
}

func TestPodTemplateLabels(t *testing.T) {
	tests := []struct {
		name     string
		fiLabels map[string]string
		expect   map[string]string
	}{
		{
			name:     "no user labels keeps only the operator-managed ones",
			fiLabels: nil,
			expect: map[string]string{
				"app":                         testDSName,
				common.IntegrityPodLabelKey:   "",
				common.IntegrityOwnerLabelKey: testFIName,
			},
		},
		{
			name:     "user labels are merged with the operator-managed ones",
			fiLabels: map[string]string{"team": "security", "monitoring": "enabled"},
			expect: map[string]string{
				"team":                        "security",
				"monitoring":                  "enabled",
				"app":                         testDSName,
				common.IntegrityPodLabelKey:   "",
				common.IntegrityOwnerLabelKey: testFIName,
			},
		},
		{
			// The CRD rejects these keys at admission, so this only covers the safety net.
			name: "operator-managed keys win over colliding user keys",
			fiLabels: map[string]string{
				"app":                         "hijacked",
				common.IntegrityPodLabelKey:   "hijacked",
				common.IntegrityOwnerLabelKey: "hijacked",
				"team":                        "security",
			},
			expect: map[string]string{
				"team":                        "security",
				"app":                         testDSName,
				common.IntegrityPodLabelKey:   "",
				common.IntegrityOwnerLabelKey: testFIName,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := podTemplateLabels(testDSName, testFileIntegrity(tt.fiLabels, nil))
			if !reflect.DeepEqual(got, tt.expect) {
				t.Errorf("unexpected labels: got %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestPodTemplateLabelsDoesNotMutateSpec(t *testing.T) {
	fi := testFileIntegrity(map[string]string{"team": "security"}, nil)
	podTemplateLabels(testDSName, fi)

	if len(fi.Spec.Labels) != 1 {
		t.Errorf("podTemplateLabels must not write back into the spec, got %v", fi.Spec.Labels)
	}
}

func TestPodTemplateAnnotations(t *testing.T) {
	tests := []struct {
		name          string
		fiAnnotations map[string]string
		expect        map[string]string
	}{
		{
			// A nil return is required: an empty non-nil map is not DeepEqual to nil, which
			// would make updateDSAnnotations report drift on every reconcile.
			name:          "nil when unset",
			fiAnnotations: nil,
			expect:        nil,
		},
		{
			name:          "nil when empty",
			fiAnnotations: map[string]string{},
			expect:        nil,
		},
		{
			name:          "user annotations are copied through",
			fiAnnotations: map[string]string{"example.com/scrape": "true"},
			expect:        map[string]string{"example.com/scrape": "true"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := podTemplateAnnotations(testFileIntegrity(nil, tt.fiAnnotations))
			if !reflect.DeepEqual(got, tt.expect) {
				t.Errorf("unexpected annotations: got %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestAideDaemonsetPodTemplateMetadata(t *testing.T) {
	fi := testFileIntegrity(
		map[string]string{"team": "security"},
		map[string]string{"example.com/scrape": "true"},
	)
	ds := aideDaemonset(testDSName, fi, "operator-image")

	if got := ds.Spec.Template.Labels["team"]; got != "security" {
		t.Errorf("expected custom label on the pod template, got labels %v", ds.Spec.Template.Labels)
	}
	if got := ds.Spec.Template.Annotations["example.com/scrape"]; got != "true" {
		t.Errorf("expected custom annotation on the pod template, got annotations %v",
			ds.Spec.Template.Annotations)
	}
	for _, key := range []string{"app", common.IntegrityPodLabelKey, common.IntegrityOwnerLabelKey} {
		if _, ok := ds.Spec.Template.Labels[key]; !ok {
			t.Errorf("operator-managed label %q missing from the pod template, got %v",
				key, ds.Spec.Template.Labels)
		}
	}

	// The DaemonSet selector is immutable after creation, so custom labels must never reach it.
	expectedSelector := map[string]string{"app": testDSName}
	if !reflect.DeepEqual(ds.Spec.Selector.MatchLabels, expectedSelector) {
		t.Errorf("custom labels must not leak into the DaemonSet selector: got %v, want %v",
			ds.Spec.Selector.MatchLabels, expectedSelector)
	}
}

// currentDaemonSet returns a DaemonSet whose pod template metadata is already in sync with fi,
// standing in for one the operator created on an earlier reconcile.
func currentDaemonSet(fi *v1alpha1.FileIntegrity) *appsv1.DaemonSet {
	return aideDaemonset(testDSName, fi, "operator-image")
}

func TestUpdateDSLabels(t *testing.T) {
	tests := []struct {
		name            string
		updatedFILabels map[string]string
		wantUpdate      bool
	}{
		{
			name:            "no change reports no update",
			updatedFILabels: map[string]string{"team": "security"},
			wantUpdate:      false,
		},
		{
			name:            "added label reports an update",
			updatedFILabels: map[string]string{"team": "security", "tier": "platform"},
			wantUpdate:      true,
		},
		{
			name:            "changed label value reports an update",
			updatedFILabels: map[string]string{"team": "compliance"},
			wantUpdate:      true,
		},
		{
			name:            "removed label reports an update",
			updatedFILabels: nil,
			wantUpdate:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds := currentDaemonSet(testFileIntegrity(map[string]string{"team": "security"}, nil))
			updated := testFileIntegrity(tt.updatedFILabels, nil)

			if got := updateDSLabels(ds, updated, logr.Discard()); got != tt.wantUpdate {
				t.Errorf("updateDSLabels returned %v, want %v", got, tt.wantUpdate)
			}

			expected := podTemplateLabels(testDSName, updated)
			if !reflect.DeepEqual(ds.Spec.Template.Labels, expected) {
				t.Errorf("DaemonSet labels not converged: got %v, want %v",
					ds.Spec.Template.Labels, expected)
			}
		})
	}
}

func TestUpdateDSAnnotations(t *testing.T) {
	tests := []struct {
		name                 string
		updatedFIAnnotations map[string]string
		wantUpdate           bool
	}{
		{
			name:                 "no change reports no update",
			updatedFIAnnotations: map[string]string{"example.com/scrape": "true"},
			wantUpdate:           false,
		},
		{
			name:                 "added annotation reports an update",
			updatedFIAnnotations: map[string]string{"example.com/scrape": "true", "example.com/port": "8080"},
			wantUpdate:           true,
		},
		{
			name:                 "removed annotation reports an update",
			updatedFIAnnotations: nil,
			wantUpdate:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds := currentDaemonSet(testFileIntegrity(nil, map[string]string{"example.com/scrape": "true"}))
			updated := testFileIntegrity(nil, tt.updatedFIAnnotations)

			if got := updateDSAnnotations(ds, updated, logr.Discard()); got != tt.wantUpdate {
				t.Errorf("updateDSAnnotations returned %v, want %v", got, tt.wantUpdate)
			}

			expected := podTemplateAnnotations(updated)
			if !reflect.DeepEqual(ds.Spec.Template.Annotations, expected) {
				t.Errorf("DaemonSet annotations not converged: got %v, want %v",
					ds.Spec.Template.Annotations, expected)
			}
		})
	}
}

// A FileIntegrity with no custom metadata must not make the reconciler see perpetual drift,
// which would restart the daemon pods on every pass.
func TestUpdateDSMetadataIsStableWhenUnset(t *testing.T) {
	fi := testFileIntegrity(nil, nil)
	ds := currentDaemonSet(fi)

	if updateDSLabels(ds, fi, logr.Discard()) {
		t.Error("updateDSLabels reported drift for an unchanged FileIntegrity")
	}
	if updateDSAnnotations(ds, fi, logr.Discard()) {
		t.Error("updateDSAnnotations reported drift for an unchanged FileIntegrity")
	}
}
