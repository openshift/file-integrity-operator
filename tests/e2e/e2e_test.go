package e2e

import (
	goctx "context"
	"strings"
	"testing"
	"time"

	"github.com/pkg/errors"

	"github.com/davecgh/go-spew/spew"

	framework "github.com/operator-framework/operator-sdk/pkg/test"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	fileintv1alpha1 "github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
	"github.com/openshift/file-integrity-operator/pkg/controller/fileintegrity"
)

const (
	testIntegrityName = "test-check"
	testConfName      = "test-conf"
	testConfDataKey   = "conf"
)

var testAideConfig = `@@define DBDIR /hostroot/etc/kubernetes
# Comment added to differ from default and trigger a re-init
@@define LOGDIR /hostroot/etc/kubernetes
database=file:@@{DBDIR}/aide.db.gz
database_out=file:@@{DBDIR}/aide.db.gz
gzip_dbout=yes
verbose=5
report_url=file:@@{LOGDIR}/aide.log
report_url=stdout
ALLXTRAHASHES = sha1+rmd160+sha256+sha512+tiger
EVERYTHING = R+ALLXTRAHASHES
NORMAL = p+i+n+u+g+s+m+c+acl+selinux+xattrs+sha512
DIR = p+i+n+u+g+acl+selinux+xattrs
PERMS = p+u+g+acl+selinux+xattrs
LOG = p+u+g+n+S+acl+selinux+xattrs
CONTENT = sha512+ftype
CONTENT_EX = sha512+ftype+p+u+g+n+acl+selinux+xattrs
DATAONLY =  p+n+u+g+s+acl+selinux+xattrs+sha512

/hostroot/boot/        CONTENT_EX
/hostroot/root/\..* PERMS
/hostroot/root/   CONTENT_EX
!/hostroot/usr/src/
!/hostroot/usr/tmp/

/hostroot/usr/    CONTENT_EX

# OpenShift specific excludes
!/hostroot/opt/
!/hostroot/var
!/hostroot/etc/NetworkManager/system-connections/
!/hostroot/etc/mtab$
!/hostroot/etc/.*~
!/hostroot/etc/kubernetes/static-pod-resources
!/hostroot/etc/kubernetes/aide.*
!/hostroot/etc/docker/certs.d
!/hostroot/etc/selinux/targeted

# Catch everything else in /etc
/hostroot/etc/    CONTENT_EX`

// setupTest sets up the operator and waits for AIDE to roll out
func setupTest(t *testing.T) (*framework.Framework, *framework.TestCtx, string) {
	testctx := setupTestRequirements(t)
	namespace, err := testctx.GetNamespace()
	if err != nil {
		t.Errorf("could not get namespace: %v", err)
	}
	testctx.AddCleanupFn(cleanUp(t, namespace))

	setupFileIntegrityOperatorCluster(t, testctx)
	f := framework.Global

	t.Log("Creating FileIntegrity object")
	testIntegrityCheck := &fileintv1alpha1.FileIntegrity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testIntegrityName,
			Namespace: namespace,
		},
		Spec: fileintv1alpha1.FileIntegritySpec{
			NodeSelector: map[string]string{
				"node-role.kubernetes.io/worker": "",
			},
			Config: fileintv1alpha1.FileIntegrityConfig{},
		},
	}
	cleanupOptions := framework.CleanupOptions{
		TestContext:   testctx,
		Timeout:       cleanupTimeout,
		RetryInterval: cleanupRetryInterval,
	}
	err = f.Client.Create(goctx.TODO(), testIntegrityCheck, &cleanupOptions)
	if err != nil {
		t.Errorf("could not create fileintegrity object: %v", err)
	}

	dsName := common.GetDaemonSetName(testIntegrityCheck.Name)
	err = waitForDaemonSet(daemonSetIsReady(f.KubeClient, dsName, namespace))
	if err != nil {
		t.Errorf("Timed out waiting for DaemonSet %s", dsName)
	}

	var lastErr error
	pollErr := wait.PollImmediate(time.Second, 5*time.Minute, func() (bool, error) {
		numWorkers, err := getNumberOfWorkerNodes(f.KubeClient)
		if err != nil {
			lastErr = err
			return false, nil
		}
		numReplicas, err := getDSReplicas(f.KubeClient, dsName, namespace)
		if err != nil {
			lastErr = err
			return false, nil
		}

		if numWorkers != numReplicas {
			lastErr = errors.Errorf("The number of worker nodes (%d) doesn't match the DS replicas (%d)", numWorkers, numReplicas)
			return false, nil
		}
		return true, nil
	})
	if pollErr != nil {
		t.Errorf("error confirming DS replica amount: (%v) (%v)", pollErr, lastErr)
	}

	// wait to go active
	err = waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Error("Timed out waiting for scan status to go Active")
	}
	return f, testctx, namespace
}

func updateFileIntegrityConfig(t *testing.T, f *framework.Framework, integrityName, configMapName, namespace, key string, interval, timeout time.Duration) {
	var lastErr error
	pollErr := wait.PollImmediate(interval, timeout, func() (bool, error) {
		fileIntegrity := &fileintv1alpha1.FileIntegrity{}
		err := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: integrityName, Namespace: namespace}, fileIntegrity)
		if err != nil {
			lastErr = err
			return false, nil
		}
		fileIntegrityCopy := fileIntegrity.DeepCopy()
		fileIntegrityCopy.Spec = fileintv1alpha1.FileIntegritySpec{
			Config: fileintv1alpha1.FileIntegrityConfig{
				Name:      configMapName,
				Namespace: namespace,
				Key:       key,
			},
		}

		err = f.Client.Update(goctx.TODO(), fileIntegrityCopy)
		if err != nil {
			lastErr = err
			return false, nil
		}
		return true, nil
	})
	if pollErr != nil {
		t.Errorf("Error updating FileIntegrity for user-conf: (%s) (%s)", pollErr, lastErr)
	}
}

func createTestConfigMap(t *testing.T, f *framework.Framework, integrityName, configMapName, namespace, key, data string) {
	// create a test AIDE config configMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
		},
		Data: map[string]string{
			key: data,
		},
	}
	_, err := f.KubeClient.CoreV1().ConfigMaps(namespace).Create(cm)
	if err != nil {
		t.Error(err)
	}

	// update FileIntegrity config spec to point to the configMap
	updateFileIntegrityConfig(t, f, integrityName, configMapName, namespace, key, time.Second, 2*time.Minute)
}

func waitForScanStatusWithTimeout(t *testing.T, f *framework.Framework, namespace, name string, targetStatus fileintv1alpha1.FileIntegrityStatusPhase, interval, timeout time.Duration) error {
	exampleFileIntegrity := &fileintv1alpha1.FileIntegrity{}
	// retry and ignore errors until timeout
	err := wait.Poll(interval, timeout, func() (bool, error) {
		getErr := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: name, Namespace: namespace}, exampleFileIntegrity)
		if getErr != nil {
			t.Logf("Retrying. Got error: %v\n", getErr)
			return false, nil
		}

		if exampleFileIntegrity.Status.Phase == targetStatus {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Logf("FileIntegrity never reached expected phase (%s)\n", targetStatus)
		return err
	}
	t.Logf("FileIntegrity ready (%s)\n", exampleFileIntegrity.Status.Phase)
	return nil
}

func waitForFailedStatusForNode(t *testing.T, f *framework.Framework, namespace, name, node string, interval, timeout time.Duration) (*fileintv1alpha1.NodeStatus, error) {
	exampleFileIntegrity := &fileintv1alpha1.FileIntegrity{}
	foundStatus := &fileintv1alpha1.NodeStatus{}
	// retry and ignore errors until timeout
	err := wait.Poll(interval, timeout, func() (bool, error) {
		getErr := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: name, Namespace: namespace}, exampleFileIntegrity)
		if getErr != nil {
			t.Logf("Retrying. Got error: %v\n", getErr)
			return false, nil
		}

		for _, status := range exampleFileIntegrity.Status.Statuses {
			if status.Condition == fileintv1alpha1.NodeConditionFailed && status.NodeName == node {
				foundStatus = &status
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Logf("status.nodeStatus for node %s not seen \n", node)
		return nil, err
	}

	return foundStatus, nil
}

// waitForScanStatus will poll until the fileintegrity that we're looking for reaches a certain status, or until
// a timeout is reached.
func waitForScanStatus(t *testing.T, f *framework.Framework, namespace, name string, targetStatus fileintv1alpha1.FileIntegrityStatusPhase) error {
	return waitForScanStatusWithTimeout(t, f, namespace, name, targetStatus, retryInterval, timeout)
}

func pollUntilConfigMapDataMatches(t *testing.T, f *framework.Framework, namespace, name, key, expected string, interval, timeout time.Duration) error {
	return wait.PollImmediate(interval, timeout, func() (bool, error) {
		cm, getErr := f.KubeClient.CoreV1().ConfigMaps(namespace).Get(name, metav1.GetOptions{})
		if getErr != nil {
			t.Logf("Retrying. Got error: %v\n", getErr)
			return false, nil
		}
		if cm.Data[key] == expected {
			return true, nil
		}
		return false, nil
	})
}

func pollUntilConfigMapExists(t *testing.T, f *framework.Framework, namespace, name string, interval, timeout time.Duration) (map[string]string, error) {
	var data map[string]string
	err := wait.PollImmediate(interval, timeout, func() (bool, error) {
		cm, getErr := f.KubeClient.CoreV1().ConfigMaps(namespace).Get(name, metav1.GetOptions{})
		if getErr != nil {
			t.Logf("Retrying. Got error: %v\n", getErr)
			return false, nil
		}
		data = cm.Data
		return true, nil
	})
	return data, err
}

func containsUncompressedScanFailLog(data map[string]string) bool {
	content, ok := data[common.IntegrityLogContentKey]
	if !ok {
		return false
	}
	return strings.Contains(content, "/hostroot/etc/kubernetes/cloud.conf")
}

func editFileOnNodes(f *framework.Framework, namespace string) error {
	daemonSet, err := f.KubeClient.AppsV1().DaemonSets(namespace).Create(modifyFileDaemonset(namespace))
	if err != nil {
		return err
	}

	spew.Printf("modifyDS created: %#v\n", daemonSet)
	err = waitForDaemonSet(daemonSetExists(f.KubeClient, daemonSet.Name, namespace))
	if err != nil {
		return err
	}
	if err := waitForDaemonSet(daemonSetWasScheduled(f.KubeClient, daemonSet.Name, namespace)); err != nil {
		return err
	}

	time.Sleep(10 * time.Second)
	if err := f.KubeClient.AppsV1().DaemonSets(namespace).Delete(daemonSet.Name, &metav1.DeleteOptions{}); err != nil {
		return err
	}
	return nil
}

func cleanNodes(f *framework.Framework, namespace string) error {
	ds, err := f.KubeClient.AppsV1().DaemonSets(namespace).Create(cleanAideDaemonset(namespace))
	if err != nil {
		return err
	}

	spew.Printf("cleanDS created: %#v\n", ds)
	err = waitForDaemonSet(daemonSetExists(f.KubeClient, ds.Name, namespace))
	if err != nil {
		return err
	}

	if err := waitForDaemonSet(daemonSetWasScheduled(f.KubeClient, ds.Name, namespace)); err != nil {
		return err
	}

	time.Sleep(10 * time.Second)
	if err := f.KubeClient.AppsV1().DaemonSets(namespace).Delete(ds.Name, &metav1.DeleteOptions{}); err != nil {
		return err
	}
	return nil
}

func TestFileIntegrityLog(t *testing.T) {
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
			data, err := pollUntilConfigMapExists(t, f, status.ResultConfigMapNamespace, status.ResultConfigMapName, time.Second, time.Minute*5)
			if err != nil {
				t.Errorf("Timeout waiting for log configMap '%s'", status.ResultConfigMapName)
			}

			if !containsUncompressedScanFailLog(data) {
				t.Errorf("configMap '%s' does not have a failure log. Got: %#v", status.ResultConfigMapName, data)
			}
		}
	}
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
