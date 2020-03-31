package e2e

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fileintv1alpha1 "github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
	"github.com/openshift/file-integrity-operator/pkg/controller/fileintegrity"
)

func TestFileIntegrityLogAndReinitDatabase(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()

	// wait to go active.
	err := waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after deploying it")
	assertNodesConditionIsSuccess(t, f, namespace, testIntegrityName, 2*time.Second, 5*time.Minute)

	// modify a file on a node
	err = editFileOnNodes(f, namespace)
	if err != nil {
		t.Errorf("Timeout waiting on node file edit")
	}

	// log collection should create a configmap for each node's report after the scan runs again
	nodes, err := f.KubeClient.CoreV1().Nodes().List(metav1.ListOptions{
		LabelSelector: "node-role.kubernetes.io/worker",
	})
	if err != nil {
		t.Error(err)
	}
	for _, node := range nodes.Items {
		// check the FI status for a failed condition for the node
		status, err := waitForFailedStatusForNode(t, f, namespace, testIntegrityName, node.Name, time.Second, time.Minute*5)
		if err != nil {
			t.Errorf("Timeout waiting for a failed status condition for node '%s'", node.Name)
		} else {
			if status.FilesChanged != 1 {
				t.Errorf("Expected one file to change, got %d", status.FilesChanged)
			}
			data, err := pollUntilConfigMapExists(t, f, status.ResultConfigMapNamespace, status.ResultConfigMapName, time.Second, time.Minute*5)
			if err != nil {
				t.Errorf("Timeout waiting for log configMap '%s'", status.ResultConfigMapName)
			}

			if !containsUncompressedScanFailLog(data) {
				t.Errorf("configMap '%s' does not have a failure log. Got: %#v", status.ResultConfigMapName, data)
			}
		}
	}
	reinitFileIntegrityDatabase(t, f, testIntegrityName, namespace, time.Second, 2*time.Minute)

	// wait to go active.
	err = waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after re-initializing the database")
	assertNodesConditionIsSuccess(t, f, namespace, testIntegrityName, 2*time.Second, 5*time.Minute)
}

// TestFileIntegrityConfigurationStatus tests the following:
// - Deployment of operator and resource
// - Successful transition from Initializing to Active
// - Update of the AIDE configuration
// - Successful transition to Initialization back to Active after update
func TestFileIntegrityConfigurationStatus(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()

	createTestConfigMap(t, f, testIntegrityName, testConfName, namespace, testConfDataKey, testAideConfig)

	// wait to go active.
	err := waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	if err := pollUntilConfigMapDataMatches(t, f, namespace, testIntegrityName, common.DefaultConfDataKey,
		testAideConfig, time.Second*5, time.Minute*5); err != nil {
		t.Errorf("Timeout waiting for configMap data to match")
	}
}

// TestFileIntegrityConfigurationIgnoreMissing tests the following:
// Deployment of operator and resources
// Successful transition from Initializing to Active
// Update of the AIDE configuration by passing a missing configmap.
// Ensure that this does not trigger a re-init.
func TestFileIntegrityConfigurationIgnoreMissing(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	// Non-existent conf
	updateFileIntegrityConfig(t, f, testIntegrityName, "fooconf", namespace, "fookey", time.Second, 2*time.Minute)

	// No re-init should happen, let this error pass.
	err := waitForScanStatusWithTimeout(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseInitializing, time.Second*5, time.Second*30)
	if err == nil {
		t.Errorf("status changed to initialization in error")
	}

	// Confirm active.
	err = waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Error(err)
	}

	if err := pollUntilConfigMapDataMatches(t, f, namespace, testIntegrityName, common.DefaultConfDataKey,
		fileintegrity.DefaultAideConfig, time.Second*5, time.Minute*5); err != nil {
		t.Errorf("Timeout waiting for configMap data to match")
	}
}
