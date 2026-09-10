package fileintegrity

import (
	"context"
	"reflect"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/openshift/file-integrity-operator/pkg/common"
)

const (
	// Names of the operator-managed operand NetworkPolicies.
	networkPolicyDefaultDeny    = "file-integrity-operator-operands-default-deny"
	networkPolicyAllowDNSEgress = "file-integrity-operator-operands-allow-dns-egress"
	networkPolicyAllowEgress    = "file-integrity-operator-operands-allow-egress"

	// dnsNamespaceLabelKey is the label the cluster applies to every namespace;
	// selecting openshift-dns by it admits egress to the CoreDNS pods.
	dnsNamespaceLabelKey = "kubernetes.io/metadata.name"
	dnsNamespace         = "openshift-dns"

	// dnsPort is the port CoreDNS pods listen on. NetworkPolicy egress matches
	// the destination pod port, not the DNS Service port (53).
	dnsPort = int32(5353)
)

// operandPodSelector selects every operand pod owned by the operator. Scanner
// and re-init pods both carry this marker, so the policies govern them all.
func operandPodSelector() metav1.LabelSelector {
	return metav1.LabelSelector{
		MatchLabels: map[string]string{common.IntegrityPodLabelKey: ""},
	}
}

// defaultDenyNetworkPolicy denies all ingress and egress for operand pods. The
// allow policies reopen only what the operands need.
func defaultDenyNetworkPolicy(ns string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      networkPolicyDefaultDeny,
			Namespace: ns,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: operandPodSelector(),
			// Both policy types set with no rules below denies all ingress and
			// egress; the allow policies add traffic back.
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
		},
	}
}

// allowDNSEgressNetworkPolicy allows operand pods to reach cluster DNS. It is a
// standard building block from the network-policy guidance; the allow-all egress
// policy subsumes it, but it is kept to document intent.
func allowDNSEgressNetworkPolicy(ns string) *networkingv1.NetworkPolicy {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	port := intstr.FromInt32(dnsPort)
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      networkPolicyAllowDNSEgress,
			Namespace: ns,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: operandPodSelector(),
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{dnsNamespaceLabelKey: dnsNamespace},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcp, Port: &port},
						{Protocol: &udp, Port: &port},
					},
				},
			},
		},
	}
}

// allowEgressNetworkPolicy allows all egress from operand pods. Egress cannot be
// meaningfully restricted here: the API server runs on the host network with a
// dynamic IP/port and is not selectable by NetworkPolicy. A single empty egress
// rule allows all egress.
func allowEgressNetworkPolicy(ns string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      networkPolicyAllowEgress,
			Namespace: ns,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: operandPodSelector(),
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      []networkingv1.NetworkPolicyEgressRule{{}},
		},
	}
}

// operandNetworkPolicies returns the full set of operator-managed operand
// NetworkPolicies for the given namespace.
func operandNetworkPolicies(ns string) []*networkingv1.NetworkPolicy {
	return []*networkingv1.NetworkPolicy{
		defaultDenyNetworkPolicy(ns),
		allowDNSEgressNetworkPolicy(ns),
		allowEgressNetworkPolicy(ns),
	}
}

// reconcileNetworkPolicies ensures the operand NetworkPolicies exist and match
// the desired spec. It uses get-then-create/update (no list or watch), matching
// the operator's RBAC grant, and is idempotent and safe to call on every
// reconcile.
func (r *FileIntegrityReconciler) reconcileNetworkPolicies(ctx context.Context, logger logr.Logger) error {
	for _, desired := range operandNetworkPolicies(common.FileIntegrityNamespace) {
		if err := r.reconcileNetworkPolicy(ctx, desired, logger); err != nil {
			return err
		}
	}
	return nil
}

func (r *FileIntegrityReconciler) reconcileNetworkPolicy(ctx context.Context, desired *networkingv1.NetworkPolicy, logger logr.Logger) error {
	found := &networkingv1.NetworkPolicy{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, found)
	if err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		logger.Info("Creating operand NetworkPolicy", "NetworkPolicy.Name", desired.Name)
		if createErr := r.Client.Create(ctx, desired); createErr != nil && !errors.IsAlreadyExists(createErr) {
			return createErr
		}
		return nil
	}

	if reflect.DeepEqual(found.Spec, desired.Spec) {
		return nil
	}
	logger.Info("Updating operand NetworkPolicy", "NetworkPolicy.Name", desired.Name)
	found.Spec = desired.Spec
	return r.Client.Update(ctx, found)
}
