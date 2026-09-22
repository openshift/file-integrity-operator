package e2e

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/file-integrity-operator/tests/framework"
	libgocrypto "github.com/openshift/library-go/pkg/crypto"
)

func TestMain(m *testing.M) {
	framework.MainEntry(m)
}

func TestOperatorHonorsClusterTLSProfile(t *testing.T) {
	// Deploy the operator like every other e2e test does. setupTest creates a
	// fresh namespace and deploys the operator (plus its metrics Service) into
	// it, registering cleanup. Skipping this - as an earlier version of this
	// test did - left f.OperatorNamespace pointing at the kubeconfig default
	// namespace ("default"), where no operator or metrics Service exists, so
	// every metrics-endpoint assertion below curled a nonexistent
	// metrics.default.svc and looped until the suite's global timeout.
	f, testctx, namespace := setupTest(t)
	defer testctx.Cleanup()

	// Point the framework at the namespace the operator was actually deployed
	// into. Every helper below (AssertMetricsEndpoint*, GetOperatorPods,
	// WaitForDeployment) reads f.OperatorNamespace; without TEST_OPERATOR_NAMESPACE
	// set (the CI case) it otherwise stays as the kubeconfig default.
	f.OperatorNamespace = namespace

	// Register the APIServer type on the framework's dynamic client scheme.
	// setupTestRequirements registers configv1.ClusterOperator but not
	// APIServer, which this test reads/updates, so register it explicitly.
	if err := framework.AddToFrameworkScheme(configv1.Install, &configv1.APIServerList{}); err != nil {
		t.Fatalf("failed to add configv1 scheme: %s", err)
	}

	// Fetch the cluster APIServer resource.
	apiServer, err := f.GetClusterAPIServer()
	if err != nil {
		t.Fatalf("failed to get APIServer cluster resource: %s", err)
	}
	t.Logf("Original TLS adherence policy: %q", apiServer.Spec.TLSAdherence)

	// Skip if the cluster is older than OCP 4.22, which is the minimum
	// version that supports the tlsAdherence field on the APIServer resource.
	atLeast422, err := f.IsOCPVersionAtLeast(4, 22)
	if err != nil {
		t.Fatalf("failed to check cluster version: %s", err)
	}
	if !atLeast422 {
		t.Skip("cluster is older than OCP 4.22, tlsAdherence is not supported")
	}

	// Skip if the TLSAdherence feature gate isn't enabled: without it,
	// tlsAdherence is absent from the APIServer CRD schema, so the
	// StrictAllComponents write below would be silently dropped and this
	// test's assertions could never pass - not a flake, a guaranteed
	// failure. TLSAdherence is Tech Preview as of OCP 4.22 and is not
	// enabled by default (e.g. this repo's own e2e-aws CI cluster doesn't
	// enable it), so this skip is expected to trigger there today.
	// TestOperatorUsesDefaultTLSWhenAdherenceIsNotStrict below covers the
	// reachable default-adherence path instead, and needs no feature gate.
	tlsAdherenceEnabled, err := f.IsTLSAdherenceFeatureGateEnabled()
	if err != nil {
		t.Fatalf("failed to check TLSAdherence feature gate: %s", err)
	}
	if !tlsAdherenceEnabled {
		t.Skip("cluster does not have the TLSAdherence feature gate enabled, tlsAdherence writes would be silently dropped")
	}

	// Verify the metrics endpoint matches the current cluster TLS configuration.
	expectedTLSVersion := f.GetExpectedMinTLSVersion(apiServer)
	t.Logf("Expected minimum TLS version before change: %s", expectedTLSVersion)
	if err := f.AssertMetricsEndpointMinTLSVersion(expectedTLSVersion); err != nil {
		t.Fatalf("metrics endpoint TLS version check failed before change: %s", err)
	}

	// Record the current operator pod UID so we can detect when it restarts.
	operatorPods, err := f.GetOperatorPods()
	if err != nil {
		t.Fatalf("failed to get operator pods: %s", err)
	}
	if len(operatorPods) == 0 {
		t.Fatal("no operator pods found")
	}
	originalPod := operatorPods[0]
	t.Logf("Original operator pod UID: %s", originalPod.UID)

	// Change the APIServer TLS configuration to strict adherence with the
	// Modern profile (TLS 1.3) so we can verify the operator enforces a
	// stricter TLS configuration when required.
	t.Log("Updating APIServer to strict adherence with Modern TLS profile")
	apiServer, err = f.GetClusterAPIServer()
	if err != nil {
		t.Fatalf("failed to get APIServer for update: %s", err)
	}
	apiServer.Spec.TLSAdherence = configv1.TLSAdherencePolicyStrictAllComponents
	apiServer.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
		Type:   configv1.TLSProfileModernType,
		Modern: &configv1.ModernTLSProfile{},
	}
	if err := f.Client.Update(t.Context(), apiServer); err != nil {
		t.Fatalf("failed to update APIServer TLS configuration: %s", err)
	}

	// Wait for the operator pod to restart. The TLS profile poll loop
	// should detect the change and trigger a graceful shutdown.
	t.Log("Waiting for operator pod to restart after TLS profile change")
	if err := f.WaitForOperatorPodRestart(originalPod); err != nil {
		t.Fatalf("operator pod did not restart after TLS profile change: %s", err)
	}

	// Wait for the operator deployment to be fully available.
	if err := f.WaitForDeployment("file-integrity-operator", 1, framework.RetryInterval, framework.Timeout); err != nil {
		t.Fatalf("operator did not become ready after TLS profile change: %s", err)
	}

	// Verify the metrics endpoint now uses the updated TLS version.
	apiServer, err = f.GetClusterAPIServer()
	if err != nil {
		t.Fatalf("failed to get APIServer after update: %s", err)
	}
	expectedTLSVersion = f.GetExpectedMinTLSVersion(apiServer)
	t.Logf("Expected minimum TLS version after change: %s", expectedTLSVersion)
	if err := f.AssertMetricsEndpointMinTLSVersion(expectedTLSVersion); err != nil {
		t.Fatalf("metrics endpoint TLS version check failed after change: %s", err)
	}

	// Verify connections capped below the new minimum are rejected. This
	// proves the floor was actually raised — without it, a server that
	// silently ignores the profile still passes the positive check above
	// because curl negotiates the highest mutually supported version.
	t.Log("Verifying metrics endpoint rejects TLS 1.2 connections")
	if err := f.AssertMetricsEndpointRejectsTLSVersion("1.2"); err != nil {
		t.Fatalf("metrics endpoint accepted a TLS 1.2 connection despite Modern (TLS 1.3) profile: %s", err)
	}
}

// TestOperatorUsesDefaultTLSWhenAdherenceIsNotStrict verifies the operator
// correctly ignores the cluster-wide TLS security profile - continuing to
// serve its own historical, hardcoded (Intermediate) TLS defaults - when
// the APIServer's TLSAdherence policy is not StrictAllComponents, the only
// value that opts a component into honoring the cluster profile (see
// libgocrypto.ShouldHonorClusterTLSProfile).
//
// Unlike TestOperatorHonorsClusterTLSProfile, this test never writes
// apiServer.Spec.TLSAdherence and does not require the TLSAdherence Tech
// Preview feature gate: it exercises exactly the configuration every
// real-world cluster is in today; TLSAdherence unset/absent
// (TLSAdherencePolicyNoOpinion), including this repo's own e2e-aws CI
// cluster. TestOperatorHonorsClusterTLSProfile is skipped there, so this is
// the only one of the two tests that provides actual CI coverage right now.
func TestOperatorUsesDefaultTLSWhenAdherenceIsNotStrict(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	defer testctx.Cleanup()
	f.OperatorNamespace = namespace

	if err := framework.AddToFrameworkScheme(configv1.Install, &configv1.APIServerList{}); err != nil {
		t.Fatalf("failed to add configv1 scheme: %s", err)
	}

	apiServer, err := f.GetClusterAPIServer()
	if err != nil {
		t.Fatalf("failed to get APIServer cluster resource: %s", err)
	}
	t.Logf("Current TLS adherence policy: %q", apiServer.Spec.TLSAdherence)

	// This test specifically validates the "not adhering" path. If the
	// cluster's adherence policy already honors the cluster profile (e.g.
	// left over from a previous TestOperatorHonorsClusterTLSProfile run on
	// a long-lived cluster - neither test restores the original spec on
	// completion), that's a different scenario already covered by the
	// other test - skip rather than asserting the wrong thing.
	if libgocrypto.ShouldHonorClusterTLSProfile(apiServer.Spec.TLSAdherence) {
		t.Skip("cluster's TLSAdherence policy already honors the cluster profile; this test validates the non-adhering default path")
	}

	// Record the pod so we can tell when the change below causes a
	// restart. The TLS profile watcher runs unconditionally (regardless of
	// whether the operator ends up honoring the profile) and restarts on
	// any raw cluster profile change - see newTLSProfileWatcher's doc
	// comment in cmd/manager/operator.go - so a restart is still expected
	// here even though adherence stays non-strict.
	operatorPods, err := f.GetOperatorPods()
	if err != nil {
		t.Fatalf("failed to get operator pods: %s", err)
	}
	if len(operatorPods) == 0 {
		t.Fatal("no operator pods found")
	}
	originalPod := operatorPods[0]
	t.Logf("Original operator pod UID: %s", originalPod.UID)

	// Change tlsSecurityProfile to Modern - a stable, always-settable
	// field, no feature gate needed - deliberately leaving TLSAdherence
	// untouched. A server that wrongly honored this despite non-strict
	// adherence would raise its floor to TLS 1.3; the assertions below
	// confirm it doesn't.
	t.Log("Updating APIServer tlsSecurityProfile to Modern, deliberately leaving TLSAdherence untouched")
	apiServer, err = f.GetClusterAPIServer()
	if err != nil {
		t.Fatalf("failed to get APIServer for update: %s", err)
	}
	apiServer.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
		Type:   configv1.TLSProfileModernType,
		Modern: &configv1.ModernTLSProfile{},
	}
	if err := f.Client.Update(t.Context(), apiServer); err != nil {
		t.Fatalf("failed to update APIServer TLS configuration: %s", err)
	}

	t.Log("Waiting for operator pod to restart in reaction to the raw profile change")
	if err := f.WaitForOperatorPodRestart(originalPod); err != nil {
		t.Fatalf("operator pod did not restart after TLS profile change: %s", err)
	}
	if err := f.WaitForDeployment("file-integrity-operator", 1, framework.RetryInterval, framework.Timeout); err != nil {
		t.Fatalf("operator did not become ready after TLS profile change: %s", err)
	}

	// After the restart, the operator must still be using its own
	// Intermediate defaults, not the cluster's Modern profile.
	apiServer, err = f.GetClusterAPIServer()
	if err != nil {
		t.Fatalf("failed to get APIServer after update: %s", err)
	}
	expectedTLSVersion := f.GetExpectedMinTLSVersion(apiServer)
	if expectedTLSVersion != "TLSv1.2" {
		t.Fatalf("test invariant broken: expected minimum TLS version should still resolve to the Intermediate default (TLSv1.2) when adherence is not strict, got %s", expectedTLSVersion)
	}
	t.Logf("Expected minimum TLS version after tlsSecurityProfile change (should still be ignored): %s", expectedTLSVersion)
	if err := f.AssertMetricsEndpointMinTLSVersion(expectedTLSVersion); err != nil {
		t.Fatalf("metrics endpoint TLS version check failed after change: %s", err)
	}

	// Verify a connection capped at TLS 1.2 still succeeds - proving the
	// floor genuinely was NOT raised to 1.3, not just that 1.2-or-higher
	// was negotiated uncapped (which a wrongly-Modern server would also
	// satisfy, since curl always negotiates the highest mutually supported
	// version). This is the actual proof that Modern was ignored.
	t.Log("Verifying metrics endpoint still accepts TLS 1.2 connections")
	if err := f.AssertMetricsEndpointAcceptsTLSVersion("1.2"); err != nil {
		t.Fatalf("metrics endpoint rejected a TLS 1.2 connection despite non-strict TLS adherence: %s", err)
	}
}
