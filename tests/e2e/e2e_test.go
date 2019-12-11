package e2e

import (
	"bytes"
	goctx "context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	fileintv1alpha1 "github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
	"github.com/openshift/file-integrity-operator/pkg/controller/fileintegrity"
	framework "github.com/operator-framework/operator-sdk/pkg/test"
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

	err = waitForDaemonSet(daemonSetIsReady(f.KubeClient, common.DaemonSetName, namespace))
	if err != nil {
		t.Error(err)
	}

	// wait to go active
	err = waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Error(err)
	}
	return f, testctx, namespace
}

func updateFileIntegrityConfig(t *testing.T, f *framework.Framework, integrityName, configMapName, namespace, key string) {
	fileIntegrity := &fileintv1alpha1.FileIntegrity{}
	err := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: integrityName, Namespace: namespace}, fileIntegrity)
	if err != nil {
		t.Error(err)
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
		t.Error(err)
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
	updateFileIntegrityConfig(t, f, integrityName, configMapName, namespace, key)
}

func waitForScanStatusWithTimeout(t *testing.T, f *framework.Framework, namespace, name string, targetStatus fileintv1alpha1.FileIntegrityStatusPhase, interval, timeout time.Duration) error {
	exampleFileIntegrity := &fileintv1alpha1.FileIntegrity{}
	var lastErr error
	// retry and ignore errors until timeout
	timeouterr := wait.Poll(interval, timeout, func() (bool, error) {
		lastErr = f.Client.Get(goctx.TODO(), types.NamespacedName{Name: name, Namespace: namespace}, exampleFileIntegrity)
		if lastErr != nil {
			if apierrors.IsNotFound(lastErr) {
				t.Logf("Waiting for availability of %s compliancescan\n", name)
				return false, nil
			}
			t.Logf("Retrying. Got error: %v\n", lastErr)
			return false, nil
		}

		if exampleFileIntegrity.Status.Phase == targetStatus {
			return true, nil
		}
		t.Logf("Waiting for run of %s fileintegrity (%s)\n", name, exampleFileIntegrity.Status.Phase)
		return false, nil
	})
	// Error in function call
	if lastErr != nil {
		return lastErr
	}
	// Timeout
	if timeouterr != nil {
		return timeouterr
	}
	t.Logf("ComplianceScan ready (%s)\n", exampleFileIntegrity.Status.Phase)
	return nil
}

// waitForScanStatus will poll until the fileintegrity that we're looking for reaches a certain status, or until
// a timeout is reached.
func waitForScanStatus(t *testing.T, f *framework.Framework, namespace, name string, targetStatus fileintv1alpha1.FileIntegrityStatusPhase) error {
	return waitForScanStatusWithTimeout(t, f, namespace, name, targetStatus, retryInterval, timeout)
}

// TestFileIntegrityConfigurationStatus tests the following:
// - Deployment of operator and resource
// - Successful transition from Initializing to Active
// - Update of the AIDE configuration
// - Successful transition to Initialization back to Active after update
func TestFileIntegrityConfigurationStatus(t *testing.T) {
	f, testctx, namespace := setupTest(t)
	defer testctx.Cleanup()

	createTestConfigMap(t, f, testIntegrityName, testConfName, namespace, testConfDataKey, testAideConfig)

	// wait for an initialization period.
	err := waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseInitializing)
	if err != nil {
		t.Error(err)
	}

	// wait to go active.
	err = waitForScanStatus(t, f, namespace, testIntegrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Error(err)
	}

	// confirm that the active configMap reflects the config update
	defaultConfMap, err := f.KubeClient.CoreV1().ConfigMaps(namespace).Get(common.DefaultConfigMapName, metav1.GetOptions{})
	if err != nil {
		t.Error(err)
	}
	if !bytes.Equal([]byte(defaultConfMap.Data[common.DefaultConfDataKey]), []byte(testAideConfig)) {
		t.Logf("current: %s", defaultConfMap.Data[common.DefaultConfDataKey])
		t.Logf("intended: %s", testAideConfig)
		t.Error("user-provided AIDE configuration did not apply")
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

	// Non-existent conf
	updateFileIntegrityConfig(t, f, testIntegrityName, "fooconf", namespace, "fookey")

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

	// confirm we still have the default conf
	defaultConfMap, err := f.KubeClient.CoreV1().ConfigMaps(namespace).Get(common.DefaultConfigMapName, metav1.GetOptions{})
	if err != nil {
		t.Error(err)
	}
	if !bytes.Equal([]byte(defaultConfMap.Data[common.DefaultConfDataKey]), []byte(fileintegrity.DefaultAideConfig)) {
		t.Logf("current: %s", defaultConfMap.Data[common.DefaultConfDataKey])
		t.Logf("intended: %s", fileintegrity.DefaultAideConfig)
		t.Error("user-provided AIDE configuration did not apply")
	}
}
