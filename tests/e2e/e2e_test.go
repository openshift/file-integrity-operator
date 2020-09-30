package e2e

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	framework "github.com/operator-framework/operator-sdk/pkg/test"
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
	defer logContainerOutput(t, f, namespace, testIntegrityName)

	// wait to go active.
	err := waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after deploying it")
	assertNodesConditionIsSuccess(t, f, namespace, 2*time.Second, 5*time.Minute)

	// modify a file on a node
	err = editFileOnNodes(f, namespace)
	if err != nil {
		t.Errorf("Timeout waiting on node file edit")
	}

	// log collection should create a configmap for each node's report after the scan runs again
	nodes, err := f.KubeClient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{
		LabelSelector: nodeWorkerRoleLabelKey,
	})
	if err != nil {
		t.Error(err)
	}
	for _, node := range nodes.Items {
		// check the FI status for a failed condition for the node
		result, err := waitForFailedResultForNode(t, f, namespace, testIntegrityName, node.Name, time.Second, time.Minute*5)
		if err != nil {
			t.Errorf("Timeout waiting for a failed status condition for node '%s'", node.Name)
		} else {
			if result.FilesChanged != 1 {
				t.Errorf("Expected one file to change, got %d", result.FilesChanged)
			}
			data, err := pollUntilConfigMapExists(t, f, result.ResultConfigMapNamespace, result.ResultConfigMapName, time.Second, time.Minute*5)
			if err != nil {
				t.Errorf("Timeout waiting for log configMap '%s'", result.ResultConfigMapName)
			}

			if !containsUncompressedScanFailLog(data) {
				t.Errorf("configMap '%s' does not have a failure log. Got: %#v", result.ResultConfigMapName, data)
			}
		}
	}
	reinitFileIntegrityDatabase(t, f, testIntegrityName, namespace, time.Second, 2*time.Minute)

	// wait to go active.
	err = waitForSuccessiveScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after re-initializing the database")
	assertNodesConditionIsSuccess(t, f, namespace, 2*time.Second, 5*time.Minute)
}

func TestFileIntegrityConfigurationRevert(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testIntegrityName)

	// Install the different config
	createTestConfigMap(t, f, testIntegrityName, testConfName, namespace, testConfDataKey, testAideConfig)
	// wait to go active.
	err := waitForSuccessiveScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after deploying it")
	assertNodesConditionIsSuccess(t, f, namespace, 2*time.Second, 5*time.Minute)

	// modify a file on a node
	err = editFileOnNodes(f, namespace)
	if err != nil {
		t.Errorf("Timeout waiting on node file edit")
	}

	// log collection should create a configmap for each node's report after the scan runs again
	nodes, err := f.KubeClient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{
		LabelSelector: nodeWorkerRoleLabelKey,
	})
	if err != nil {
		t.Error(err)
	}
	for _, node := range nodes.Items {
		// check the FI status for a failed condition for the node
		result, err := waitForFailedResultForNode(t, f, namespace, testIntegrityName, node.Name, time.Second, time.Minute*5)
		if err != nil {
			t.Errorf("Timeout waiting for a failed status condition for node '%s'", node.Name)
		} else {
			if result.FilesChanged != 1 {
				t.Errorf("Expected one file to change, got %d", result.FilesChanged)
			}
			data, err := pollUntilConfigMapExists(t, f, result.ResultConfigMapNamespace, result.ResultConfigMapName, time.Second, time.Minute*5)
			if err != nil {
				t.Errorf("Timeout waiting for log configMap '%s'", result.ResultConfigMapName)
			}

			if !containsUncompressedScanFailLog(data) {
				t.Errorf("configMap '%s' does not have a failure log. Got: %#v", result.ResultConfigMapName, data)
			}
		}
	}

	// We've staged a fail, now unset the config.
	t.Log("Unsetting the config")
	fileIntegrity := &fileintv1alpha1.FileIntegrity{}
	err = f.Client.Get(context.TODO(), types.NamespacedName{Name: testIntegrityName, Namespace: namespace}, fileIntegrity)
	if err != nil {
		t.Errorf("failed to retrieve FI object: %v\n", err)
	}

	fileIntegrityCopy := fileIntegrity.DeepCopy()
	fileIntegrityCopy.Spec.Config = fileintv1alpha1.FileIntegrityConfig{
		GracePeriod: defaultTestGracePeriod,
	}

	err = f.Client.Update(context.TODO(), fileIntegrityCopy)
	if err != nil {
		t.Errorf("failed to update FI object: %v\n", err)
	}

	// wait to go active.
	err = waitForSuccessiveScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after a re-init")
	assertNodesConditionIsSuccess(t, f, namespace, 2*time.Second, 5*time.Minute)
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
	defer logContainerOutput(t, f, namespace, testIntegrityName)

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
	defer logContainerOutput(t, f, namespace, testIntegrityName)

	// Non-existent conf
	updateFileIntegrityConfig(t, f, testIntegrityName, "fooconf", namespace, "fookey", time.Second, 2*time.Minute)

	// No re-init should happen, let this error pass.
	err := waitForScanStatusWithTimeout(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseInitializing, time.Second*5, time.Second*30, 0)
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

// This is meant to test that the operator can react to expected changes (MCO changes)
// in such a way that it'll automatically re-initialize the database after the changes
// have taken place.
// This will:
// - Create a FileIntegrity object
// - assert that the status is success
// - Create a MachineConfig object
// - Wait for the nodes to be ready
// - assert that the status is success
//
// NOTE: This test is run last because it modifies the node and causes restarts
func TestFileIntegrityAcceptsExpectedChange(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testIntegrityName)

	// wait to go active.
	err := waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after deploying it")
	assertNodesConditionIsSuccess(t, f, namespace, 2*time.Second, 5*time.Minute)

	// Create MCFG
	mcfg := getTestMcfg(t)
	cleanupOptions := framework.CleanupOptions{
		TestContext:   testctx,
		Timeout:       cleanupTimeout,
		RetryInterval: cleanupRetryInterval,
	}
	err = f.Client.Create(context.TODO(), mcfg, &cleanupOptions)
	if err != nil {
		t.Errorf("Cannot create a test MC: %v", err)
	}

	// Wait some time... The machineConfigs take some time to kick in.
	time.Sleep(30 * time.Second)

	// Wait for nodes to be ready
	if err = waitForNodesToBeReady(f); err != nil {
		t.Errorf("Timeout waiting for nodes")
	}

	// wait to go active.
	err = waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after expected changes")
	assertNodesConditionIsSuccess(t, f, namespace, 5*time.Second, 5*time.Minute)
}

func TestFileIntegrityChangeGracePeriod(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testIntegrityName)

	// wait to go active.
	err := waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	oldPodList, err := getFiDsPods(f, testIntegrityName, namespace)
	if err != nil {
		t.Errorf("Error retrieving DS pods")
	}

	// get daemonSet, make sure there's the default sleep
	defaultSleep := fmt.Sprintf("--interval=%d", defaultTestGracePeriod)
	err = assertDSPodHasArg(t, f, testIntegrityName, namespace, defaultSleep, time.Second*5, time.Minute*5)
	if err != nil {
		t.Errorf("pod spec didn't contain the expected sleep: %v\n", err)
	}
	t.Log("The pod spec contains the default grace period")

	// change the config
	fileIntegrity := &fileintv1alpha1.FileIntegrity{}
	err = f.Client.Get(context.TODO(), types.NamespacedName{Name: testIntegrityName, Namespace: namespace}, fileIntegrity)
	if err != nil {
		t.Errorf("failed to retrieve FI object: %v\n", err)
	}

	newGracePeriod := 30
	fileIntegrityCopy := fileIntegrity.DeepCopy()
	fileIntegrityCopy.Spec = fileintv1alpha1.FileIntegritySpec{
		Config: fileintv1alpha1.FileIntegrityConfig{
			GracePeriod: newGracePeriod,
		},
	}

	err = f.Client.Update(context.TODO(), fileIntegrityCopy)
	if err != nil {
		t.Errorf("failed to update FI object: %v\n", err)
	}

	// make sure the daemonSet pods now has the sleep we want
	modifiedSleep := fmt.Sprintf("--interval=%d", newGracePeriod)
	err = assertDSPodHasArg(t, f, testIntegrityName, namespace, modifiedSleep, time.Second*5, time.Minute*5)
	if err != nil {
		t.Errorf("spec didn't contain the expected sleep: %v\n", err)
	}
	t.Log("The spec contains the modified grace period")

	// make sure the DS restarted by first making sure at least one of the original pods
	// went away, then waiting until the DS is ready again
	err = waitUntilPodsAreGone(t, f.Client.Client, oldPodList, time.Second*5, time.Minute*5)
	if err != nil {
		t.Errorf("The old pods were not shut down\n")
	}

	dsName := common.GetDaemonSetName(testIntegrityName)
	err = waitForDaemonSet(daemonSetIsReady(f.KubeClient, dsName, namespace))
	if err != nil {
		t.Errorf("Timed out waiting for DaemonSet %s", dsName)
	}
}

func TestFileIntegrityChangeDebug(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testIntegrityName)

	// wait to go active.
	err := waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	oldPodList, err := getFiDsPods(f, testIntegrityName, namespace)
	if err != nil {
		t.Errorf("Error retrieving DS pods")
	}

	// get daemonSet, make sure there's the default debug
	defaultDebug := fmt.Sprintf("--debug=%s", strconv.FormatBool(false))
	err = assertDSPodHasArg(t, f, testIntegrityName, namespace, defaultDebug, time.Second*5, time.Minute*5)
	if err != nil {
		t.Errorf("pod spec didn't contain the expected debug setting: %v\n", err)
	}
	t.Log("The pod spec contains the default debug setting")

	// change the config
	fileIntegrity := &fileintv1alpha1.FileIntegrity{}
	err = f.Client.Get(context.TODO(), types.NamespacedName{Name: testIntegrityName, Namespace: namespace}, fileIntegrity)
	if err != nil {
		t.Errorf("failed to retrieve FI object: %v\n", err)
	}

	fileIntegrityCopy := fileIntegrity.DeepCopy()
	fileIntegrityCopy.Spec = fileintv1alpha1.FileIntegritySpec{
		Debug: true,
	}

	err = f.Client.Update(context.TODO(), fileIntegrityCopy)
	if err != nil {
		t.Errorf("failed to update FI object: %v\n", err)
	}

	// make sure the daemonSet pods now has the debug setting we want
	modifiedDebug := fmt.Sprintf("--debug=%s", strconv.FormatBool(true))
	err = assertDSPodHasArg(t, f, testIntegrityName, namespace, modifiedDebug, time.Second*5, time.Minute*5)
	if err != nil {
		t.Errorf("spec didn't contain the expected debug setting: %v\n", err)
	}
	t.Log("The spec contains the modified debug setting")

	// make sure the DS restarted by first making sure at least one of the original pods
	// went away, then waiting until the DS is ready again
	err = waitUntilPodsAreGone(t, f.Client.Client, oldPodList, time.Second*5, time.Minute*5)
	if err != nil {
		t.Errorf("The old pods were not shut down\n")
	}

	dsName := common.GetDaemonSetName(testIntegrityName)
	err = waitForDaemonSet(daemonSetIsReady(f.KubeClient, dsName, namespace))
	if err != nil {
		t.Errorf("Timed out waiting for DaemonSet %s", dsName)
	}
}

func TestFileIntegrityBadConfig(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()

	// Install the different config
	createTestConfigMap(t, f, testIntegrityName, testConfName, namespace, testConfDataKey, brokenAideConfig)

	// wait to go to error state.
	err := waitForScanStatusWithTimeout(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseError, retryInterval, timeout, 1)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}
}

func TestFileIntegrityTolerations(t *testing.T) {
	f, testctx, namespace := setupTolerationTest(t)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testIntegrityName)

	// wait to go active.
	err := waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after deploying it")
	assertNodesConditionIsSuccess(t, f, namespace, 2*time.Second, 5*time.Minute)
}
