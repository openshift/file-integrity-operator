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
	// Must call setupTest, not skip it: doing so previously left
	// f.OperatorNamespace pointing at a namespace with no operator, and
	// every metrics assertion below looped until the suite timeout.
	f, testctx, namespace := setupTest(t)
	defer testctx.Cleanup()

	// Helpers below read f.OperatorNamespace; without TEST_OPERATOR_NAMESPACE
	// set (the CI case) it otherwise stays the kubeconfig default namespace.
	f.OperatorNamespace = namespace

	// APIServer isn't registered by setupTestRequirements; needed for the
	// reads/updates below.
	if err := framework.AddToFrameworkScheme(configv1.Install, &configv1.APIServerList{}); err != nil {
		t.Fatalf("failed to add configv1 scheme: %s", err)
	}

	apiServer, err := f.GetClusterAPIServer()
	if err != nil {
		t.Fatalf("failed to get APIServer cluster resource: %s", err)
	}
	t.Logf("Original TLS adherence policy: %q", apiServer.Spec.TLSAdherence)

	// tlsAdherence requires OCP >= 4.22.
	atLeast422, err := f.IsOCPVersionAtLeast(4, 22)
	if err != nil {
		t.Fatalf("failed to check cluster version: %s", err)
	}
	if !atLeast422 {
		t.Skip("cluster is older than OCP 4.22, tlsAdherence is not supported")
	}

	// TLSAdherence is Tech Preview and disabled by default (including this
	// repo's CI cluster); without it, the StrictAllComponents write below is
	// silently dropped and the assertions below could never pass.
	// TestOperatorUsesDefaultTLSWhenAdherenceIsNotStrict covers the
	// reachable default path and needs no feature gate.
	tlsAdherenceEnabled, err := f.IsTLSAdherenceFeatureGateEnabled()
	if err != nil {
		t.Fatalf("failed to check TLSAdherence feature gate: %s", err)
	}
	if !tlsAdherenceEnabled {
		t.Skip("cluster does not have the TLSAdherence feature gate enabled, tlsAdherence writes would be silently dropped")
	}

	expectedTLSVersion := f.GetExpectedMinTLSVersion(apiServer)
	t.Logf("Expected minimum TLS version before change: %s", expectedTLSVersion)
	if err := f.AssertMetricsEndpointMinTLSVersion(expectedTLSVersion); err != nil {
		t.Fatalf("metrics endpoint TLS version check failed before change: %s", err)
	}

	operatorPods, err := f.GetOperatorPods()
	if err != nil {
		t.Fatalf("failed to get operator pods: %s", err)
	}
	if len(operatorPods) == 0 {
		t.Fatal("no operator pods found")
	}
	originalPod := operatorPods[0]
	t.Logf("Original operator pod UID: %s", originalPod.UID)

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

	// The poll loop detects the change and triggers a restart.
	t.Log("Waiting for operator pod to restart after TLS profile change")
	if err := f.WaitForOperatorPodRestart(originalPod); err != nil {
		t.Fatalf("operator pod did not restart after TLS profile change: %s", err)
	}

	if err := f.WaitForDeployment("file-integrity-operator", 1, framework.RetryInterval, framework.Timeout); err != nil {
		t.Fatalf("operator did not become ready after TLS profile change: %s", err)
	}

	apiServer, err = f.GetClusterAPIServer()
	if err != nil {
		t.Fatalf("failed to get APIServer after update: %s", err)
	}
	expectedTLSVersion = f.GetExpectedMinTLSVersion(apiServer)
	t.Logf("Expected minimum TLS version after change: %s", expectedTLSVersion)
	if err := f.AssertMetricsEndpointMinTLSVersion(expectedTLSVersion); err != nil {
		t.Fatalf("metrics endpoint TLS version check failed after change: %s", err)
	}

	// Also verify TLS 1.2 is rejected: a server that ignores the profile
	// would still pass the check above, since curl negotiates the highest
	// mutually supported version.
	t.Log("Verifying metrics endpoint rejects TLS 1.2 connections")
	if err := f.AssertMetricsEndpointRejectsTLSVersion("1.2"); err != nil {
		t.Fatalf("metrics endpoint accepted a TLS 1.2 connection despite Modern (TLS 1.3) profile: %s", err)
	}
}

// TestOperatorUsesDefaultTLSWhenAdherenceIsNotStrict verifies the operator
// keeps its own Intermediate defaults when TLSAdherence is not
// StrictAllComponents, the only value that opts into the cluster profile
// (see libgocrypto.ShouldHonorClusterTLSProfile). Unlike
// TestOperatorHonorsClusterTLSProfile, it needs no feature gate and covers
// this repo's CI cluster, which runs with adherence unset.
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

	// Skip if the cluster already honors the profile (e.g. leftover from a
	// previous TestOperatorHonorsClusterTLSProfile run) - covered by that
	// test instead.
	if libgocrypto.ShouldHonorClusterTLSProfile(apiServer.Spec.TLSAdherence) {
		t.Skip("cluster's TLSAdherence policy already honors the cluster profile; this test validates the non-adhering default path")
	}

	// A restart is still expected: the poll watcher restarts on any raw
	// profile change regardless of whether adherence honors it (see
	// newTLSProfileWatcher's doc comment in cmd/manager/operator.go).
	operatorPods, err := f.GetOperatorPods()
	if err != nil {
		t.Fatalf("failed to get operator pods: %s", err)
	}
	if len(operatorPods) == 0 {
		t.Fatal("no operator pods found")
	}
	originalPod := operatorPods[0]
	t.Logf("Original operator pod UID: %s", originalPod.UID)

	// Deliberately leave TLSAdherence untouched: a server that wrongly
	// honors this would raise its floor to TLS 1.3.
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

	// Also verify TLS 1.2 still succeeds: proves the floor wasn't silently
	// raised, since curl would otherwise still negotiate 1.2-or-higher.
	t.Log("Verifying metrics endpoint still accepts TLS 1.2 connections")
	if err := f.AssertMetricsEndpointAcceptsTLSVersion("1.2"); err != nil {
		t.Fatalf("metrics endpoint rejected a TLS 1.2 connection despite non-strict TLS adherence: %s", err)
	}
}
