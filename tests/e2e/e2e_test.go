package e2e

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	framework "github.com/operator-framework/operator-sdk/pkg/test"

	"k8s.io/apimachinery/pkg/types"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fileintv1alpha1 "github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
	"github.com/openshift/file-integrity-operator/pkg/controller/fileintegrity"
)

func TestFileIntegrityLogAndReinitDatabase(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	testName := testIntegrityNamePrefix + "-reinitdb"
	setupFileIntegrity(t, f, testctx, testName, namespace)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testName)

	// wait to go active.
	err := waitForScanStatus(t, f, namespace, testName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after deploying it")
	assertNodesConditionIsSuccess(t, f, testName, namespace, 2*time.Second, 5*time.Minute)

	t.Log("Asserting that we have OK node condition events")
	assertNodeOKStatusEvents(t, f, namespace, 2*time.Second, 5*time.Minute)

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
		result, err := waitForFailedResultForNode(t, f, namespace, testName, node.Name, time.Second, time.Minute*5)
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
	reinitFileIntegrityDatabase(t, f, testName, namespace, time.Second, 2*time.Minute)

	// wait to go active.
	err = waitForSuccessiveScanStatus(t, f, namespace, testName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after re-initializing the database")
	assertNodesConditionIsSuccess(t, f, testName, namespace, 2*time.Second, 5*time.Minute)
}

func TestFileIntegrityConfigurationRevert(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	testName := testIntegrityNamePrefix + "-configrevert"
	setupFileIntegrity(t, f, testctx, testName, namespace)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testName)

	// Install the different config
	createTestConfigMap(t, f, testName, testConfName, namespace, testConfDataKey, testAideConfig)
	// update FileIntegrity config spec to point to the configMap
	updateFileIntegrityConfig(t, f, testName, testConfName, namespace, testConfDataKey, time.Second, 2*time.Minute)

	// wait to go active.
	err := waitForSuccessiveScanStatus(t, f, namespace, testName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after deploying it")
	assertNodesConditionIsSuccess(t, f, testName, namespace, 2*time.Second, 5*time.Minute)

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
		result, err := waitForFailedResultForNode(t, f, namespace, testName, node.Name, time.Second, time.Minute*5)
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
	err = f.Client.Get(context.TODO(), types.NamespacedName{Name: testName, Namespace: namespace}, fileIntegrity)
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
	err = waitForSuccessiveScanStatus(t, f, namespace, testName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after a re-init")
	assertNodesConditionIsSuccess(t, f, testName, namespace, 2*time.Second, 5*time.Minute)
}

// TestFileIntegrityConfigurationStatus tests the following:
// - Deployment of operator and resource
// - Successful transition from Initializing to Active
// - Update of the AIDE configuration
// - Successful transition to Initialization back to Active after update
// - Confirms Active and Init FileIntegrityStatus events
func TestFileIntegrityConfigurationStatus(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	testName := testIntegrityNamePrefix + "-configstatus"
	setupFileIntegrity(t, f, testctx, testName, namespace)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testName)

	// Confirm there was an event logged for the transition to Active.
	// We are not guaranteed events for the initial states (Pending, Initializing) because the first status update
	// could be any of them, but also Active.
	if err := waitForFIStatusEvent(t, f, namespace, testName,
		string(fileintv1alpha1.PhaseActive)); err != nil {
		t.Error(err)
	}

	createTestConfigMap(t, f, testName, testConfName, namespace, testConfDataKey, testAideConfig)

	// update FileIntegrity config spec to point to the configMap
	updateFileIntegrityConfig(t, f, testName, testConfName, namespace, testConfDataKey, time.Second, 2*time.Minute)

	// wait to go active.
	if err := waitForScanStatus(t, f, namespace, testName, fileintv1alpha1.PhaseActive); err != nil {
		t.Error(err)
	}

	if err := pollUntilConfigMapDataMatches(t, f, namespace, testName, common.DefaultConfDataKey,
		testAideConfig, time.Second*5, time.Minute*5); err != nil {
		t.Error(err)
	}

	// Confirm that there was an event logged for the transition to Init. It could possibly be from earlier, but the
	// point is to show that the Init event can appear. Chances that it will appear by this point are high.
	if err := waitForFIStatusEvent(t, f, namespace, testName,
		string(fileintv1alpha1.PhaseInitializing)); err != nil {
		t.Error(err)
	}
}

// TestFileIntegrityConfigurationIgnoreMissing tests the following:
// Deployment of operator and resources
// Successful transition from Initializing to Active
// Update of the AIDE configuration by passing a missing configmap.
// Ensure that this does not trigger a re-init.
func TestFileIntegrityConfigurationIgnoreMissing(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	testName := testIntegrityNamePrefix + "-configignoremissing"
	setupFileIntegrity(t, f, testctx, testName, namespace)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testName)

	// Non-existent conf
	t.Log("Updating file integrity with non-existent user-config")
	updateFileIntegrityConfig(t, f, testName, "fooconf", namespace, "fookey", time.Second, 2*time.Minute)

	// No re-init should happen, let this error pass.
	err := waitForScanStatusWithTimeout(t, f, namespace, testName, fileintv1alpha1.PhaseInitializing, time.Second*5, time.Second*30, 0)
	if err == nil {
		t.Errorf("status changed to initialization in error")
	}

	// Confirm active.
	err = waitForSuccessiveScanStatus(t, f, namespace, testName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Error(err)
	}

	if err := pollUntilConfigMapDataMatches(t, f, namespace, testName, common.DefaultConfDataKey,
		fileintegrity.DefaultAideConfig, time.Second*5, time.Minute*5); err != nil {
		t.Errorf("Timeout waiting for configMap data to match")
	}
}

func TestFileIntegrityChangeGracePeriod(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	testName := testIntegrityNamePrefix + "-graceperiod"
	setupFileIntegrity(t, f, testctx, testName, namespace)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testName)

	// wait to go active.
	err := waitForScanStatus(t, f, namespace, testName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	oldPodList, err := getFiDsPods(f, testName, namespace)
	if err != nil {
		t.Errorf("Error retrieving DS pods")
	}

	// get daemonSet, make sure there's the default sleep
	defaultSleep := fmt.Sprintf("--interval=%d", defaultTestGracePeriod)
	err = assertDSPodHasArg(t, f, testName, namespace, defaultSleep, time.Second*5, time.Minute*5)
	if err != nil {
		t.Errorf("pod spec didn't contain the expected sleep: %v\n", err)
	}
	t.Log("The pod spec contains the default grace period")

	// change the config
	fileIntegrity := &fileintv1alpha1.FileIntegrity{}
	err = f.Client.Get(context.TODO(), types.NamespacedName{Name: testName, Namespace: namespace}, fileIntegrity)
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
	err = assertDSPodHasArg(t, f, testName, namespace, modifiedSleep, time.Second*5, time.Minute*5)
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

	dsName := common.GetDaemonSetName(testName)
	err = waitForDaemonSet(daemonSetIsReady(f.KubeClient, dsName, namespace))
	if err != nil {
		t.Errorf("Timed out waiting for DaemonSet %s", dsName)
	}
}

func TestFileIntegrityChangeDebug(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	testName := testIntegrityNamePrefix + "-changedebug"
	setupFileIntegrity(t, f, testctx, testName, namespace)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testName)

	startValue := true
	modifiedValue := false

	// wait to go active.
	err := waitForScanStatus(t, f, namespace, testName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	oldPodList, err := getFiDsPods(f, testName, namespace)
	if err != nil {
		t.Errorf("Error retrieving DS pods")
	}

	// get daemonSet, make sure there's the default debug
	defaultDebug := fmt.Sprintf("--debug=%s", strconv.FormatBool(startValue))
	err = assertDSPodHasArg(t, f, testName, namespace, defaultDebug, time.Second*5, time.Minute*5)
	if err != nil {
		t.Errorf("pod spec didn't contain the expected debug setting: %v\n", err)
	}

	// change the config
	fileIntegrity := &fileintv1alpha1.FileIntegrity{}
	err = f.Client.Get(context.TODO(), types.NamespacedName{Name: testName, Namespace: namespace}, fileIntegrity)
	if err != nil {
		t.Errorf("failed to retrieve FI object: %v\n", err)
	}

	fileIntegrityCopy := fileIntegrity.DeepCopy()
	fileIntegrityCopy.Spec = fileintv1alpha1.FileIntegritySpec{
		Debug: modifiedValue,
	}

	err = f.Client.Update(context.TODO(), fileIntegrityCopy)
	if err != nil {
		t.Errorf("failed to update FI object: %v\n", err)
	}

	// make sure the daemonSet pods now has the debug setting we want
	modifiedDebug := fmt.Sprintf("--debug=%s", strconv.FormatBool(modifiedValue))
	err = assertDSPodHasArg(t, f, testName, namespace, modifiedDebug, time.Second*5, time.Minute*5)
	if err != nil {
		t.Errorf("spec didn't contain the expected debug setting: %v\n", err)
	}

	// make sure the DS restarted by first making sure at least one of the original pods
	// went away, then waiting until the DS is ready again
	err = waitUntilPodsAreGone(t, f.Client.Client, oldPodList, time.Second*5, time.Minute*5)
	if err != nil {
		t.Errorf("The old pods were not shut down\n")
	}

	dsName := common.GetDaemonSetName(testName)
	err = waitForDaemonSet(daemonSetIsReady(f.KubeClient, dsName, namespace))
	if err != nil {
		t.Errorf("Timed out waiting for DaemonSet %s", dsName)
	}
}

// TestFileIntegrityBadConfig checks that a broken AIDE config supplied to the config will result in an Error state for
// the FileIntegrity,
func TestFileIntegrityBadConfig(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	testName := testIntegrityNamePrefix + "-badconfig"
	setupFileIntegrity(t, f, testctx, testName, namespace)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testName)

	// Install the different config
	createTestConfigMap(t, f, testName, testConfName+"-broken", namespace, testConfDataKey, brokenAideConfig)

	// update FileIntegrity config spec to point to the configMap
	updateFileIntegrityConfig(t, f, testName, testConfName+"-broken", namespace, testConfDataKey, time.Second, 2*time.Minute)

	// wait to go to error state.
	err := waitForScanStatusWithTimeout(t, f, namespace, testName, fileintv1alpha1.PhaseError, retryInterval, timeout, 1)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	// Fix the config
	updateTestConfigMap(t, f, testConfName+"-broken", namespace, testConfDataKey, testAideConfig)
	// wait to go to active state.
	err = waitForScanStatusWithTimeout(t, f, namespace, testName, fileintv1alpha1.PhaseActive, retryInterval, timeout, 1)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}
}

func TestFileIntegrityTolerations(t *testing.T) {
	f, testctx, namespace := setupTolerationTest(t, testIntegrityNamePrefix+"-tolerations")
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testIntegrityNamePrefix+"-tolerations")

	// wait to go active.
	err := waitForScanStatus(t, f, namespace, testIntegrityNamePrefix+"-tolerations", fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after deploying it")
	assertNodesConditionIsSuccess(t, f, testIntegrityNamePrefix+"-tolerations", namespace, 2*time.Second, 5*time.Minute)
}

func TestFileIntegrityLogCompress(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	testName := testIntegrityNamePrefix + "-logcompress"
	setupFileIntegrity(t, f, testctx, testName, namespace)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
		if err := cleanAddedFilesOnNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testName)

	// wait to go active.
	err := waitForScanStatus(t, f, namespace, testName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after deploying it")
	assertNodesConditionIsSuccess(t, f, testName, namespace, 2*time.Second, 5*time.Minute)

	// modify files
	err = addALottaFilesOnNodes(f, namespace)
	if err != nil {
		t.Errorf("Timeout waiting on node file addition")
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
		result, err := waitForFailedResultForNode(t, f, namespace, testName, node.Name, time.Second, time.Minute*5)
		if err != nil {
			t.Errorf("Timeout waiting for a failed status condition for node '%s'", node.Name)
		} else {
			if result.FilesAdded != 20000 {
				t.Errorf("Expected 20000 files to be added, got %d", result.FilesAdded)
			}
			_, err := pollUntilConfigMapExists(t, f, result.ResultConfigMapNamespace, result.ResultConfigMapName, time.Second, time.Minute*5)
			if err != nil {
				t.Errorf("Timeout waiting for log configMap '%s'", result.ResultConfigMapName)
			}
			cm, err := f.KubeClient.CoreV1().ConfigMaps(result.ResultConfigMapNamespace).Get(context.TODO(), result.ResultConfigMapName, metav1.GetOptions{})
			if err != nil {
				t.Error(err)
			}
			if _, ok := cm.Annotations[common.CompressedLogsIndicatorLabelKey]; !ok {
				t.Errorf("configMap '%s' does not indicate compression", result.ResultConfigMapName)
			}
		}
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
	testName := testIntegrityNamePrefix + "-nodechange"
	setupFileIntegrity(t, f, testctx, testName, namespace)
	defer testctx.Cleanup()
	defer func() {
		if err := cleanNodes(f, namespace); err != nil {
			t.Fatal(err)
		}
	}()
	defer logContainerOutput(t, f, namespace, testName)

	// wait to go active.
	err := waitForScanStatus(t, f, namespace, testName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after deploying it")
	assertNodesConditionIsSuccess(t, f, testName, namespace, 2*time.Second, 5*time.Minute)

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
	err = waitForScanStatus(t, f, namespace, testName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Errorf("Timeout waiting for scan status")
	}

	t.Log("Asserting that the FileIntegrity check is in a SUCCESS state after expected changes")
	assertNodesConditionIsSuccess(t, f, testName, namespace, 5*time.Second, 5*time.Minute)
}
