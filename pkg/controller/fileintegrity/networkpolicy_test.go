package fileintegrity

import (
	"context"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
)

func TestDefaultDenyNetworkPolicy(t *testing.T) {
	np := defaultDenyNetworkPolicy(common.FileIntegrityNamespace)

	if np.Name != networkPolicyDefaultDeny {
		t.Errorf("unexpected name: got %q, want %q", np.Name, networkPolicyDefaultDeny)
	}
	if v, ok := np.Spec.PodSelector.MatchLabels[common.IntegrityPodLabelKey]; !ok || v != "" {
		t.Errorf("expected operand marker in pod selector, got matchLabels %v", np.Spec.PodSelector.MatchLabels)
	}
	// Both policy types with no rules => deny ingress and egress.
	if len(np.Spec.PolicyTypes) != 2 {
		t.Errorf("expected Ingress+Egress policy types, got %v", np.Spec.PolicyTypes)
	}
	if len(np.Spec.Ingress) != 0 || len(np.Spec.Egress) != 0 {
		t.Errorf("default-deny must have no ingress/egress rules, got ingress=%d egress=%d",
			len(np.Spec.Ingress), len(np.Spec.Egress))
	}
}

func TestAllowDNSEgressNetworkPolicy(t *testing.T) {
	np := allowDNSEgressNetworkPolicy(common.FileIntegrityNamespace)

	if np.Name != networkPolicyAllowDNSEgress {
		t.Errorf("unexpected name: got %q, want %q", np.Name, networkPolicyAllowDNSEgress)
	}
	if v, ok := np.Spec.PodSelector.MatchLabels[common.IntegrityPodLabelKey]; !ok || v != "" {
		t.Errorf("expected operand marker in pod selector, got matchLabels %v", np.Spec.PodSelector.MatchLabels)
	}
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Errorf("expected only Egress policy type, got %v", np.Spec.PolicyTypes)
	}
	if len(np.Spec.Egress) != 1 {
		t.Fatalf("expected exactly one egress rule, got %d", len(np.Spec.Egress))
	}
	rule := np.Spec.Egress[0]

	if len(rule.To) != 1 || rule.To[0].NamespaceSelector == nil {
		t.Fatalf("expected one namespaceSelector peer, got %+v", rule.To)
	}
	if got := rule.To[0].NamespaceSelector.MatchLabels[dnsNamespaceLabelKey]; got != dnsNamespace {
		t.Errorf("expected DNS namespace selector %q=%q, got %v", dnsNamespaceLabelKey, dnsNamespace,
			rule.To[0].NamespaceSelector.MatchLabels)
	}

	// Both TCP and UDP on the DNS pod port.
	var haveTCP, haveUDP bool
	for _, p := range rule.Ports {
		if p.Port == nil || p.Port.IntVal != dnsPort {
			t.Errorf("expected DNS port %d, got %v", dnsPort, p.Port)
			continue
		}
		if p.Protocol != nil && *p.Protocol == corev1.ProtocolTCP {
			haveTCP = true
		}
		if p.Protocol != nil && *p.Protocol == corev1.ProtocolUDP {
			haveUDP = true
		}
	}
	if !haveTCP || !haveUDP {
		t.Errorf("expected both TCP and UDP DNS ports, got tcp=%v udp=%v (ports=%+v)", haveTCP, haveUDP, rule.Ports)
	}
}

func TestAllowEgressNetworkPolicy(t *testing.T) {
	np := allowEgressNetworkPolicy(common.FileIntegrityNamespace)

	if np.Name != networkPolicyAllowEgress {
		t.Errorf("unexpected name: got %q, want %q", np.Name, networkPolicyAllowEgress)
	}
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Errorf("expected only Egress policy type, got %v", np.Spec.PolicyTypes)
	}
	// A single empty egress rule allows all egress.
	if len(np.Spec.Egress) != 1 {
		t.Fatalf("expected exactly one (allow-all) egress rule, got %d", len(np.Spec.Egress))
	}
	if len(np.Spec.Egress[0].To) != 0 || len(np.Spec.Egress[0].Ports) != 0 {
		t.Errorf("allow-all egress rule must have no peers/ports, got to=%d ports=%d",
			len(np.Spec.Egress[0].To), len(np.Spec.Egress[0].Ports))
	}
}

// newNetworkPolicyTestReconciler returns a reconciler backed by an in-memory
// fake client (no cluster / envtest needed).
func newNetworkPolicyTestReconciler(objs ...*networkingv1.NetworkPolicy) *FileIntegrityReconciler {
	builder := fake.NewClientBuilder().WithScheme(scheme.Scheme)
	for _, o := range objs {
		builder = builder.WithObjects(o)
	}
	return &FileIntegrityReconciler{Client: builder.Build()}
}

func listNetworkPolicies(t *testing.T, r *FileIntegrityReconciler) *networkingv1.NetworkPolicyList {
	t.Helper()
	list := &networkingv1.NetworkPolicyList{}
	if err := r.Client.List(context.TODO(), list); err != nil {
		t.Fatalf("listing network policies: %v", err)
	}
	return list
}

func TestReconcileNetworkPoliciesCreatesAll(t *testing.T) {
	r := newNetworkPolicyTestReconciler()

	if err := r.reconcileNetworkPolicies(context.TODO(), logr.Discard()); err != nil {
		t.Fatalf("reconcileNetworkPolicies: %v", err)
	}

	want := operandNetworkPolicies(common.FileIntegrityNamespace)
	list := listNetworkPolicies(t, r)
	if len(list.Items) != len(want) {
		t.Fatalf("expected %d network policies, got %d", len(want), len(list.Items))
	}
	for _, desired := range want {
		found := &networkingv1.NetworkPolicy{}
		key := types.NamespacedName{Name: desired.Name, Namespace: common.FileIntegrityNamespace}
		if err := r.Client.Get(context.TODO(), key, found); err != nil {
			t.Errorf("policy %q not created: %v", desired.Name, err)
		}
	}
}

func TestReconcileNetworkPoliciesIdempotent(t *testing.T) {
	r := newNetworkPolicyTestReconciler()

	if err := r.reconcileNetworkPolicies(context.TODO(), logr.Discard()); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	first := listNetworkPolicies(t, r)

	// A second reconcile must not error and must not create duplicates.
	if err := r.reconcileNetworkPolicies(context.TODO(), logr.Discard()); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	second := listNetworkPolicies(t, r)

	if len(second.Items) != len(first.Items) {
		t.Fatalf("reconcile not idempotent: policy count changed from %d to %d",
			len(first.Items), len(second.Items))
	}
}

func TestReconcileNetworkPolicyCorrectsDrift(t *testing.T) {
	ns := common.FileIntegrityNamespace

	// Seed a default-deny policy with a drifted spec (wrong pod selector).
	drifted := defaultDenyNetworkPolicy(ns)
	drifted.Spec.PodSelector = metav1.LabelSelector{MatchLabels: map[string]string{"drifted": "true"}}
	r := newNetworkPolicyTestReconciler(drifted)

	if err := r.reconcileNetworkPolicies(context.TODO(), logr.Discard()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	found := &networkingv1.NetworkPolicy{}
	key := types.NamespacedName{Name: networkPolicyDefaultDeny, Namespace: ns}
	if err := r.Client.Get(context.TODO(), key, found); err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}
	want := defaultDenyNetworkPolicy(ns)
	if !reflect.DeepEqual(found.Spec, want.Spec) {
		t.Errorf("drift not corrected:\n got  %+v\n want %+v", found.Spec, want.Spec)
	}
}

// TestOperandPodTemplatesCarryNetworkPolicyMarker verifies that both operand
// DaemonSet pod templates carry the marker so the operand policies select them,
// and that the marker does not leak into the immutable DaemonSet selectors.
func TestOperandPodTemplatesCarryNetworkPolicyMarker(t *testing.T) {
	fi := &v1alpha1.FileIntegrity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-fi",
			Namespace: common.FileIntegrityNamespace,
		},
	}

	scanner := aideDaemonset(common.DaemonSetName(fi.Name), fi, "operator-image")
	assertHasNetworkPolicyMarker(t, "scanner", scanner.Spec.Template.ObjectMeta.Labels)
	assertSelectorLacksMarker(t, "scanner", scanner.Spec.Selector)

	reinit := reinitAideDaemonset(common.ReinitDaemonSetName(fi.Name), fi, metav1.LabelSelector{}, "operator-image")
	assertHasNetworkPolicyMarker(t, "re-init", reinit.Spec.Template.ObjectMeta.Labels)
	assertSelectorLacksMarker(t, "re-init", reinit.Spec.Selector)
}

func assertHasNetworkPolicyMarker(t *testing.T, operand string, labels map[string]string) {
	t.Helper()
	if v, ok := labels[common.IntegrityPodLabelKey]; !ok || v != "" {
		t.Errorf("%s pod template missing NetworkPolicy marker label (labels=%v)", operand, labels)
	}
}

func assertSelectorLacksMarker(t *testing.T, operand string, selector *metav1.LabelSelector) {
	t.Helper()
	if selector == nil {
		return
	}
	if _, ok := selector.MatchLabels[common.IntegrityPodLabelKey]; ok {
		t.Errorf("%s DaemonSet selector must not carry the NetworkPolicy marker (matchLabels=%v)",
			operand, selector.MatchLabels)
	}
}
