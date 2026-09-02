package e2e

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/file-integrity-operator/tests/framework"
)

func TestMain(m *testing.M) {
	framework.MainEntry(m)
}

func TestOperatorHonorsClusterTLSProfile(t *testing.T) {
	f := framework.Global

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
	originalPodUID := operatorPods[0].UID
	t.Logf("Original operator pod UID: %s", originalPodUID)

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
	if err := f.Client.Update(context.TODO(), apiServer); err != nil {
		t.Fatalf("failed to update APIServer TLS configuration: %s", err)
	}

	// Wait for the operator pod to restart. The SecurityProfileWatcher
	// should detect the change and trigger a graceful shutdown.
	t.Log("Waiting for operator pod to restart after TLS profile change")
	if err := f.WaitForOperatorPodRestart(originalPodUID); err != nil {
		t.Fatalf("operator pod did not restart after TLS profile change: %s", err)
	}

	// Wait for the operator deployment to be fully available.
	if err := f.WaitForDeployment(framework.OperatorName, 1, framework.RetryInterval, framework.Timeout); err != nil {
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
