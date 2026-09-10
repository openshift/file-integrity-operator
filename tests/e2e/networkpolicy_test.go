package e2e

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
)

// Names mirror the unexported constants in
// pkg/controller/fileintegrity/networkpolicy.go. The e2e treats the operator as
// a black box, so they are duplicated here.
const (
	npDefaultDeny    = "file-integrity-operator-operands-default-deny"
	npAllowDNSEgress = "file-integrity-operator-operands-allow-dns-egress"
	npAllowEgress    = "file-integrity-operator-operands-allow-egress"
)

// TestFileIntegrityOperandNetworkPolicies verifies that deploying a FileIntegrity
// causes the operator to reconcile the operand NetworkPolicies, and that operands
// still function with the policies in place. Reaching the Active phase means the
// AIDE scanner pods delivered their results to the API server (result ConfigMaps)
// through the egress policies, exercising the egress path end to end.
func TestFileIntegrityOperandNetworkPolicies(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	testName := testIntegrityNamePrefix + "-networkpolicy"
	setupFileIntegrity(t, f, testctx, testName, namespace, nodeWorkerRoleLabelKey, defaultTestGracePeriod)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testName)

	// Reaching Active proves the operand pods run and can reach the API server
	// (to create result ConfigMaps) with the policies in place: egress works.
	if err := waitForScanStatus(t, f, namespace, testName, v1alpha1.PhaseActive); err != nil {
		t.Errorf("Timeout waiting for scan status with NetworkPolicies in place")
	}

	// All three operand NetworkPolicies must exist.
	for _, name := range []string{npDefaultDeny, npAllowDNSEgress, npAllowEgress} {
		np, err := f.KubeClient.NetworkingV1().NetworkPolicies(namespace).Get(
			context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("expected NetworkPolicy %q to exist: %s", name, err)
		}
		if len(np.Spec.PolicyTypes) == 0 {
			t.Errorf("NetworkPolicy %q has no policy types", name)
		}
	}

	// default-deny must select operand pods and carry no allow rules.
	dd, err := f.KubeClient.NetworkingV1().NetworkPolicies(namespace).Get(
		context.TODO(), npDefaultDeny, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dd.Spec.PodSelector.MatchLabels[common.IntegrityPodLabelKey]; !ok {
		t.Errorf("default-deny does not select operand pods: %v", dd.Spec.PodSelector.MatchLabels)
	}
	if len(dd.Spec.Ingress) != 0 || len(dd.Spec.Egress) != 0 {
		t.Errorf("default-deny must have no allow rules, got ingress=%d egress=%d",
			len(dd.Spec.Ingress), len(dd.Spec.Egress))
	}

	// DNS egress policy must open port 5353 (TCP+UDP) to openshift-dns.
	dns, err := f.KubeClient.NetworkingV1().NetworkPolicies(namespace).Get(
		context.TODO(), npAllowDNSEgress, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dns.Spec.Egress) != 1 || len(dns.Spec.Egress[0].Ports) == 0 {
		t.Fatalf("unexpected DNS egress rule shape: %+v", dns.Spec.Egress)
	}
	var haveTCP, haveUDP bool
	for _, p := range dns.Spec.Egress[0].Ports {
		if p.Port == nil || p.Port.IntVal != 5353 {
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
		t.Errorf("DNS egress should open 5353 TCP+UDP, got %+v", dns.Spec.Egress[0].Ports)
	}

	// allow-egress must be a single allow-all egress rule.
	ae, err := f.KubeClient.NetworkingV1().NetworkPolicies(namespace).Get(
		context.TODO(), npAllowEgress, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ae.Spec.Egress) != 1 || len(ae.Spec.Egress[0].To) != 0 || len(ae.Spec.Egress[0].Ports) != 0 {
		t.Errorf("allow-egress must be a single allow-all rule, got %+v", ae.Spec.Egress)
	}
}
