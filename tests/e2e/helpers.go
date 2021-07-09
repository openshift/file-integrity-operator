package e2e

import (
	"bufio"
	"bytes"
	goctx "context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/cenkalti/backoff/v4"
	igntypes "github.com/coreos/ignition/v2/config/v3_2/types"

	mcfgapi "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgconst "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	framework "github.com/operator-framework/operator-sdk/pkg/test"
	"github.com/operator-framework/operator-sdk/pkg/test/e2eutil"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/file-integrity-operator/pkg/apis"
	fileintv1alpha1 "github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
)

const (
	pollInterval            = time.Second * 2
	pollTimeout             = time.Minute * 5
	retryInterval           = time.Second * 5
	timeout                 = time.Minute * 30
	cleanupRetryInterval    = time.Second * 1
	cleanupTimeout          = time.Minute * 5
	testIntegrityNamePrefix = "e2e-test"
	testConfName            = "test-conf"
	testConfDataKey         = "conf"
	nodeWorkerRoleLabelKey  = "node-role.kubernetes.io/worker"
	mcWorkerRoleLabelKey    = "machineconfiguration.openshift.io/role"
	defaultTestGracePeriod  = 20
)

const (
	curlCMD    = "curl -ks -H \"Authorization: Bearer `cat /var/run/secrets/kubernetes.io/serviceaccount/token`\" "
	metricsURL = "https://metrics.openshift-file-integrity.svc:8585/"
	curlFIOCMD = curlCMD + metricsURL + "metrics-fio"
)

var mcLabelForWorkerRole = map[string]string{
	mcWorkerRoleLabelKey: "worker",
}

var nodeLabelForWorkerRole = map[string]string{
	nodeWorkerRoleLabelKey: "",
}

var testAideConfig = `@@define DBDIR /hostroot/etc/kubernetes
# Comment added to differ from default and trigger a re-init
@@define LOGDIR /hostroot/etc/kubernetes
database=file:@@{DBDIR}/aide.db.gz
database_out=file:@@{DBDIR}/aide.db.gz.new
gzip_dbout=yes
verbose=5
report_url=file:@@{LOGDIR}/aide.log.new
report_url=stdout
PERMS = p+u+g+acl+selinux+xattrs
CONTENT_EX = sha512+ftype+p+u+g+n+acl+selinux+xattrs

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
!/hostroot/etc/kubernetes/manifests
!/hostroot/etc/docker/certs.d
!/hostroot/etc/selinux/targeted
!/hostroot/etc/openvswitch/conf.db
!/hostroot/etc/kubernetes/cni/net.d/*

# Catch everything else in /etc
/hostroot/etc/    CONTENT_EX`

var brokenAideConfig = testAideConfig + "\n" + "NORMAL = p+i+n+u+g+s+m+c+acl+selinux+xattrs+sha513+md5+XXXXXX"

func newTestFileIntegrity(name, ns string, nodeSelector map[string]string, grace int, debug bool) *fileintv1alpha1.FileIntegrity {
	return &fileintv1alpha1.FileIntegrity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: fileintv1alpha1.FileIntegritySpec{
			NodeSelector: nodeSelector,
			Config: fileintv1alpha1.FileIntegrityConfig{
				GracePeriod: grace,
			},
			Debug: debug,
		},
	}
}

func cleanUp(namespace string) func() error {
	return func() error {
		f := framework.Global

		list, err := f.KubeClient.CoreV1().ConfigMaps(namespace).List(goctx.TODO(), metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, cm := range list.Items {
			if err := f.KubeClient.CoreV1().ConfigMaps(namespace).Delete(goctx.TODO(), cm.Name, metav1.DeleteOptions{}); err != nil {
				if !kerr.IsNotFound(err) {
					return err
				}
			}
		}
		return deleteStatusEvents(f, namespace)
	}
}

func deleteStatusEvents(f *framework.Framework, namespace string) error {
	selectors := []string{"reason=FileIntegrityStatus", "reason=NodeIntegrityStatus"}
	for _, sel := range selectors {
		eventList, err := f.KubeClient.CoreV1().Events(namespace).List(goctx.TODO(), metav1.ListOptions{FieldSelector: sel})
		if err != nil {
			return err
		}
		for _, ev := range eventList.Items {
			if err := f.KubeClient.CoreV1().Events(namespace).Delete(goctx.TODO(), ev.Name, metav1.DeleteOptions{}); err != nil {
				if !kerr.IsNotFound(err) {
					return err
				}
			}
		}
	}
	return nil
}

func setupTestRequirements(t *testing.T) *framework.Context {
	fileIntegrities := &fileintv1alpha1.FileIntegrityList{}
	nodeStatus := &fileintv1alpha1.FileIntegrityNodeStatus{}

	err := framework.AddToFrameworkScheme(apis.AddToScheme, fileIntegrities)
	if err != nil {
		t.Fatalf("TEST SETUP: failed to add custom resource scheme to framework: %v", err)
	}

	mcList := &mcfgv1.MachineConfigList{}
	err = framework.AddToFrameworkScheme(mcfgapi.Install, mcList)
	if err != nil {
		t.Fatalf("TEST SETUP: failed to add custom resource scheme to framework: %v", err)
	}

	err = framework.AddToFrameworkScheme(apis.AddToScheme, nodeStatus)
	if err != nil {
		t.Fatalf("TEST SETUP: failed to add custom resource scheme to framework: %v", err)
	}
	return framework.NewContext(t)
}

func setupFileIntegrityOperatorCluster(t *testing.T, ctx *framework.Context) {
	cleanupOptions := framework.CleanupOptions{
		TestContext:   ctx,
		Timeout:       cleanupTimeout,
		RetryInterval: cleanupRetryInterval,
	}
	err := ctx.InitializeClusterResources(&cleanupOptions)
	if err != nil {
		t.Fatalf("failed to initialize cluster resources: %v", err)
	}
	t.Log("Initialized cluster resources")
	namespace, err := ctx.GetOperatorNamespace()
	if err != nil {
		t.Fatal(err)
	}
	// get global framework variables
	f := framework.Global
	// wait for file-integrity-operator to be ready
	err = e2eutil.WaitForOperatorDeployment(t, f.KubeClient, namespace, "file-integrity-operator", 1, retryInterval, timeout)
	if err != nil {
		t.Fatal(err)
	}
}

// setupTest deploys the operator
func setupTest(t *testing.T) (*framework.Framework, *framework.Context, string) {
	testctx := setupTestRequirements(t)
	namespace, err := testctx.GetOperatorNamespace()
	if err != nil {
		t.Fatalf("could not get namespace: %v", err)
	}
	testctx.AddCleanupFn(cleanUp(namespace))

	setupFileIntegrityOperatorCluster(t, testctx)
	return framework.Global, testctx, namespace
}

// setupFileIntegrity creates the FileIntegrity instance and confirms
func setupFileIntegrity(t *testing.T, f *framework.Framework, testCtx *framework.Context, integrityName, namespace string) {
	testIntegrityCheck := newTestFileIntegrity(integrityName, namespace, nodeLabelForWorkerRole, defaultTestGracePeriod, true)

	cleanupOptions := framework.CleanupOptions{
		TestContext:   testCtx,
		Timeout:       cleanupTimeout,
		RetryInterval: cleanupRetryInterval,
	}
	err := f.Client.Create(goctx.TODO(), testIntegrityCheck, &cleanupOptions)
	if err != nil {
		t.Fatalf("Could not create FileIntegrity: %v", err)
	}

	t.Logf("Created FileIntegrity: %+v", testIntegrityCheck)

	dsName := common.GetDaemonSetName(testIntegrityCheck.Name)
	err = waitForDaemonSet(daemonSetIsReady(f.KubeClient, dsName, namespace))
	if err != nil {
		t.Fatalf("Timed out waiting for DaemonSet %s", dsName)
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
		t.Fatalf("Error confirming DS replica amount: (%v) (%v)", pollErr, lastErr)
	}

	// wait to go active
	err = waitForScanStatus(t, f, namespace, integrityName, fileintv1alpha1.PhaseActive)
	if err != nil {
		t.Fatalf("Timed out waiting for scan status to go Active")
	}
	t.Log("FileIntegrity deployed successfully")

}

func daemonSetExists(c kubernetes.Interface, name, namespace string) wait.ConditionFunc {
	return func() (bool, error) {
		_, err := c.AppsV1().DaemonSets(namespace).Get(goctx.TODO(), name, metav1.GetOptions{})
		if err != nil && !kerr.IsNotFound(err) {
			return false, err
		}
		if kerr.IsNotFound(err) {
			return false, nil
		}
		return true, nil
	}
}

func daemonSetIsReady(c kubernetes.Interface, name, namespace string) wait.ConditionFunc {
	return func() (bool, error) {
		daemonSet, err := c.AppsV1().DaemonSets(namespace).Get(goctx.TODO(), name, metav1.GetOptions{})
		if err != nil && !kerr.IsNotFound(err) {
			return false, err
		}
		if kerr.IsNotFound(err) {
			return false, nil
		}
		if daemonSet.Status.DesiredNumberScheduled != daemonSet.Status.NumberAvailable {
			return false, nil
		}
		return true, nil
	}
}

func daemonSetWasScheduled(c kubernetes.Interface, name, namespace string) wait.ConditionFunc {
	return func() (bool, error) {
		daemonSet, err := c.AppsV1().DaemonSets(namespace).Get(goctx.TODO(), name, metav1.GetOptions{})
		if err != nil && !kerr.IsNotFound(err) {
			return false, err
		}
		if kerr.IsNotFound(err) {
			return false, nil
		}
		if daemonSet.Status.DesiredNumberScheduled != daemonSet.Status.CurrentNumberScheduled {
			return false, nil
		}
		return true, nil
	}
}

func waitForDaemonSet(daemonSetCallback wait.ConditionFunc) error {
	return wait.PollImmediate(pollInterval, timeout, daemonSetCallback)
}

// This daemonSet runs a command to clear the aide content from the host
func cleanAideDaemonset(namespace string) *appsv1.DaemonSet {
	return privCommandDaemonset(namespace, "aide-clean",
		"rm -f /hostroot/etc/kubernetes/aide* || true",
	)
}

func modifyFileDaemonset(namespace string) *appsv1.DaemonSet {
	return privCommandDaemonset(namespace, "aide-modify-file",
		"echo '#foobar' >> /hostroot/etc/resolv.conf || true",
	)
}

func cleanAideAddedFilesDaemonset(namespace string) *appsv1.DaemonSet {
	return privCommandDaemonset(namespace, "aide-clean-added-files",
		"rm -f /hostroot/etc/addedbytest* || true",
	)
}

// this daemonset is to stuff the aide log with added files, to force compression.
// 20000 might not be enough
// lowered to 10000
func addLotsOfFilesDaemonset(namespace string) *appsv1.DaemonSet {
	return privCommandDaemonset(namespace, "aide-add-lots-of-files",
		"for i in `seq 1 10000`; do mktemp \"/hostroot/etc/addedbytest$i.XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX\"; done || true",
	)
}

func privCommandDaemonset(namespace, name, command string) *appsv1.DaemonSet {
	priv := true
	runAs := int64(0)

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": name,
					},
				},
				Spec: corev1.PodSpec{
					Tolerations: []corev1.Toleration{
						{
							Key:      "node-role.kubernetes.io/master",
							Operator: "Exists",
							Effect:   "NoSchedule",
						},
					},
					ServiceAccountName: common.OperatorServiceAccountName,
					Containers: []corev1.Container{
						{
							SecurityContext: &corev1.SecurityContext{
								Privileged: &priv,
								RunAsUser:  &runAs,
							},
							Name:    name,
							Image:   "busybox",
							Command: []string{"/bin/sh"},
							Args:    []string{"-c", command},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "hostroot",
									MountPath: "/hostroot",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "hostroot",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/",
								},
							},
						},
					},
					NodeSelector: nodeLabelForWorkerRole,
				},
			},
		},
	}
}

func getDSReplicas(c kubernetes.Interface, name, namespace string) (int, error) {
	daemonSet, err := c.AppsV1().DaemonSets(namespace).Get(goctx.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return 0, err
	}
	return int(daemonSet.Status.NumberAvailable), nil
}

func getNumberOfWorkerNodes(c kubernetes.Interface) (int, error) {
	listopts := metav1.ListOptions{
		LabelSelector: nodeWorkerRoleLabelKey,
	}
	nodes, err := c.CoreV1().Nodes().List(goctx.TODO(), listopts)
	if err != nil {
		return 0, err
	}
	return len(nodes.Items), nil
}

// TODO: break this up into setupTest/setupFileIntegrity steps like below.
func setupTolerationTest(t *testing.T, integrityName string) (*framework.Framework, *framework.Context, string, string) {
	taintedNodeName := ""
	testctx := setupTestRequirements(t)
	namespace, err := testctx.GetOperatorNamespace()
	if err != nil {
		t.Errorf("could not get namespace: %v", err)
	}
	f := framework.Global
	workerNodes, err := getNodesWithSelector(f, map[string]string{"node-role.kubernetes.io/worker": ""})
	if err != nil {
		t.Errorf("could not list nodes: %v", err)
	}
	taintedNode := &workerNodes[0]
	taintedNodeName = taintedNode.Name
	taintKey := "fi-e2e"
	taintVal := "val"
	taint := corev1.Taint{
		Key:    taintKey,
		Value:  taintVal,
		Effect: corev1.TaintEffectNoSchedule,
	}

	testctx.AddCleanupFn(func() error {
		return removeNodeTaint(t, f, taintedNode.Name, taintKey)
	})
	testctx.AddCleanupFn(cleanUp(namespace))
	setupFileIntegrityOperatorCluster(t, testctx)

	if err := taintNode(t, f, taintedNode, taint); err != nil {
		t.Fatal("Tainting node failed")
	}

	t.Log("Creating FileIntegrity object for Toleration tests")
	testIntegrityCheck := &fileintv1alpha1.FileIntegrity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      integrityName,
			Namespace: namespace,
		},
		Spec: fileintv1alpha1.FileIntegritySpec{
			NodeSelector: map[string]string{
				// Schedule on the tainted host
				corev1.LabelHostname: taintedNode.Labels[corev1.LabelHostname],
			},
			Config: fileintv1alpha1.FileIntegrityConfig{
				GracePeriod: defaultTestGracePeriod,
			},
			Tolerations: []corev1.Toleration{
				{
					Key:      taintKey,
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				},
			},
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
		numReplicas, err := getDSReplicas(f.KubeClient, dsName, namespace)
		if err != nil {
			lastErr = err
			return false, nil
		}

		if numReplicas != 1 {
			lastErr = errors.Errorf("The number of worker nodes (1 tainted) doesn't match the DS replicas (%d)", numReplicas)
			return false, nil
		}
		return true, nil
	})
	if pollErr != nil {
		t.Errorf("error confirming DS replica amount: (%v) (%v)", pollErr, lastErr)
	}

	return f, testctx, namespace, taintedNodeName
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

		// We only want the config to update. Preserve the other fields, like debug and node selectors
		fileIntegrityCopy := fileIntegrity.DeepCopy()
		fileIntegrityCopy.Spec.Config.Name = configMapName
		fileIntegrityCopy.Spec.Config.Namespace = namespace
		fileIntegrityCopy.Spec.Config.Key = key

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

func reinitFileIntegrityDatabase(t *testing.T, f *framework.Framework, integrityName, namespace string, interval, timeout time.Duration) {
	var lastErr error
	pollErr := wait.PollImmediate(interval, timeout, func() (bool, error) {
		fileIntegrity := &fileintv1alpha1.FileIntegrity{}
		err := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: integrityName, Namespace: namespace}, fileIntegrity)
		if err != nil {
			lastErr = err
			return false, nil
		}
		fileIntegrityCopy := fileIntegrity.DeepCopy()
		fileIntegrityCopy.Annotations = map[string]string{
			common.AideDatabaseReinitAnnotationKey: "",
		}
		err = f.Client.Update(goctx.TODO(), fileIntegrityCopy)
		if err != nil {
			lastErr = err
			return false, nil
		}
		return true, nil
	})
	if pollErr != nil {
		t.Errorf("Error adding re-init annotation to FileIntegrity: (%s) (%s)", pollErr, lastErr)
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
	_, err := f.KubeClient.CoreV1().ConfigMaps(namespace).Create(goctx.TODO(), cm, metav1.CreateOptions{})
	if err != nil {
		t.Error(err)
	}

}

func updateTestConfigMap(t *testing.T, f *framework.Framework, configMapName, namespace, key, data string) {
	// update the test AIDE configmap
	cm, err := f.KubeClient.CoreV1().ConfigMaps(namespace).Get(goctx.TODO(), configMapName, metav1.GetOptions{})
	if err != nil {
		t.Error(err)
	}
	cmCopy := cm.DeepCopy()

	cmCopy.Data[key] = data
	_, err = f.KubeClient.CoreV1().ConfigMaps(namespace).Update(goctx.TODO(), cmCopy, metav1.UpdateOptions{})
	if err != nil {
		t.Error(err)
	}
}

func waitForScanStatusWithTimeout(t *testing.T, f *framework.Framework, namespace, name string, targetStatus fileintv1alpha1.FileIntegrityStatusPhase, interval, timeout time.Duration, successiveResults int) error {
	exampleFileIntegrity := &fileintv1alpha1.FileIntegrity{}
	resultNum := 0
	// retry and ignore errors until timeout
	err := wait.Poll(interval, timeout, func() (bool, error) {
		getErr := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: name, Namespace: namespace}, exampleFileIntegrity)
		if getErr != nil {
			t.Logf("Retrying. Got error: %v\n", getErr)
			return false, nil
		}

		if exampleFileIntegrity.Status.Phase == targetStatus {
			if resultNum <= successiveResults {
				resultNum++
				t.Logf("Got (%s) result #%d out of %d needed.", targetStatus, resultNum, successiveResults)
				return false, nil
			}
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

func waitForFailedResultForNode(t *testing.T, f *framework.Framework, namespace, name, node string, interval, timeout time.Duration) (*fileintv1alpha1.FileIntegrityScanResult, error) {
	var foundResult *fileintv1alpha1.FileIntegrityScanResult
	// retry and ignore errors until timeout
	err := wait.Poll(interval, timeout, func() (bool, error) {
		nodeStatus := &fileintv1alpha1.FileIntegrityNodeStatus{}
		getErr := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: name + "-" + node, Namespace: namespace}, nodeStatus)
		if getErr != nil {
			return false, nil
		}

		if nodeStatus.LastResult.Condition == fileintv1alpha1.NodeConditionFailed {
			foundResult = &nodeStatus.LastResult
			t.Logf("failed result for node %s found, r:%d a:%d c:%d", node, foundResult.FilesRemoved,
				foundResult.FilesAdded, foundResult.FilesChanged)
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Logf("%s did not encounter failed condition", node)
		return nil, err
	}

	return foundResult, nil
}

func waitForFIStatusEvent(t *testing.T, f *framework.Framework, namespace, name, expectedMessage string) error {
	exampleFileIntegrity := &fileintv1alpha1.FileIntegrity{}
	err := wait.Poll(pollInterval, pollTimeout, func() (bool, error) {
		getErr := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: name, Namespace: namespace}, exampleFileIntegrity)
		if getErr != nil {
			t.Log(getErr)
			return false, nil
		}

		eventList, getEventErr := f.KubeClient.CoreV1().Events(namespace).List(goctx.TODO(), metav1.ListOptions{
			FieldSelector: "reason=FileIntegrityStatus",
		})

		if getEventErr != nil {
			t.Log(getEventErr)
			return false, nil
		}
		for _, item := range eventList.Items {
			if item.InvolvedObject.Name == exampleFileIntegrity.Name && item.Message == expectedMessage {
				t.Logf("Found FileIntegrityStatus event: %s", expectedMessage)
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Logf("No FileIntegrityStatus event with message \"%s\" found", expectedMessage)
		return err
	}

	return nil
}

func assertNodeOKStatusEvents(t *testing.T, f *framework.Framework, namespace string, interval, timeout time.Duration) {
	var lastErr error
	nodes, err := f.KubeClient.CoreV1().Nodes().List(goctx.TODO(), metav1.ListOptions{LabelSelector: nodeWorkerRoleLabelKey})
	if err != nil {
		t.Error(err)
		return
	}

	seenNodes := map[string]bool{}
	timeoutErr := wait.PollImmediate(interval, timeout, func() (bool, error) {
		// Node names are the key
		eventList, getEventErr := f.KubeClient.CoreV1().Events(namespace).List(goctx.TODO(), metav1.ListOptions{
			FieldSelector: "reason=NodeIntegrityStatus",
		})

		if getEventErr != nil {
			t.Log(getEventErr)
			lastErr = getEventErr
			return false, nil
		}

		// Look for an OK node status event for each node.
		for _, node := range nodes.Items {
			for _, event := range eventList.Items {
				if event.Type == corev1.EventTypeNormal && event.Message == "no changes to node "+node.Name {
					seenNodes[node.Name] = true
				}
			}
		}

		// Seen all of the node events?
		for _, node := range nodes.Items {
			if !seenNodes[node.Name] {
				return false, nil
			}
		}

		// reset error since we're good
		lastErr = nil
		return true, nil
	})
	if lastErr != nil {
		t.Error(lastErr)
		return
	}
	if timeoutErr != nil {
		t.Error(timeoutErr)
		return
	}
}

func assertNodesConditionIsSuccess(t *testing.T, f *framework.Framework, integrityName, namespace string, interval, timeout time.Duration) {
	var lastErr error
	type nodeStatus struct {
		LastProbeTime metav1.Time
		Condition     fileintv1alpha1.FileIntegrityNodeCondition
	}

	nodes, err := f.KubeClient.CoreV1().Nodes().List(goctx.TODO(), metav1.ListOptions{LabelSelector: nodeWorkerRoleLabelKey})
	if err != nil {
		t.Errorf("error listing nodes: %v", err)
	}
	timeoutErr := wait.PollImmediate(interval, timeout, func() (bool, error) {
		// Node names are the key
		latestStatuses := map[string]nodeStatus{}
		for _, node := range nodes.Items {
			fileIntegNodeStatus := &fileintv1alpha1.FileIntegrityNodeStatus{}
			getErr := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: integrityName + "-" + node.Name, Namespace: namespace}, fileIntegNodeStatus)
			if getErr != nil {
				lastErr = getErr
				return false, nil
			}

			t.Logf("%s %s", fileIntegNodeStatus.NodeName, fileIntegNodeStatus.LastResult.Condition)

			latestStatuses[fileIntegNodeStatus.NodeName] = nodeStatus{
				LastProbeTime: fileIntegNodeStatus.LastResult.LastProbeTime,
				Condition:     fileIntegNodeStatus.LastResult.Condition,
			}
		}

		// iterate gathered statuses
		for nodeName, status := range latestStatuses {
			if status.Condition != fileintv1alpha1.NodeConditionSucceeded {
				lastErr = fmt.Errorf("status.nodeStatus for node %s NOT SUCCESS: But instead %s", nodeName, status.Condition)
				return false, nil

			}
		}
		// reset error since we're good
		lastErr = nil
		return true, nil
	})
	if lastErr != nil {
		t.Errorf("ERROR: nodes weren't in good state: %s", lastErr)
	}
	if timeoutErr != nil {
		t.Errorf("ERROR: timed out waiting for node condition: %s", timeoutErr)
	}
}

func assertSingleNodeConditionIsSuccess(t *testing.T, f *framework.Framework, integrityName, nodeName, namespace string, interval, timeout time.Duration) {
	var lastErr error
	type nodeStatus struct {
		LastProbeTime metav1.Time
		Condition     fileintv1alpha1.FileIntegrityNodeCondition
	}

	nodes, err := f.KubeClient.CoreV1().Nodes().List(goctx.TODO(), metav1.ListOptions{LabelSelector: nodeWorkerRoleLabelKey})
	if err != nil {
		t.Errorf("error listing nodes: %v", err)
	}

	timeoutErr := wait.PollImmediate(interval, timeout, func() (bool, error) {
		// Node names are the key
		latestStatuses := map[string]nodeStatus{}
		for _, node := range nodes.Items {
			if node.Name != nodeName {
				continue
			}
			fileIntegNodeStatus := &fileintv1alpha1.FileIntegrityNodeStatus{}
			getErr := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: integrityName + "-" + node.Name, Namespace: namespace}, fileIntegNodeStatus)
			if getErr != nil {
				lastErr = getErr
				return false, nil
			}

			t.Logf("%s %s", fileIntegNodeStatus.NodeName, fileIntegNodeStatus.LastResult.Condition)

			latestStatuses[fileIntegNodeStatus.NodeName] = nodeStatus{
				LastProbeTime: fileIntegNodeStatus.LastResult.LastProbeTime,
				Condition:     fileIntegNodeStatus.LastResult.Condition,
			}
		}

		// iterate gathered statuses
		for name, status := range latestStatuses {
			if status.Condition != fileintv1alpha1.NodeConditionSucceeded {
				lastErr = fmt.Errorf("status.nodeStatus for node %s NOT SUCCESS: But instead %s", name, status.Condition)
				return false, nil
			}
		}
		// reset error since we're good
		lastErr = nil
		return true, nil
	})
	if lastErr != nil {
		t.Errorf("ERROR: nodes weren't in good state: %s", lastErr)
	}
	if timeoutErr != nil {
		t.Errorf("ERROR: timed out waiting for node condition: %s", timeoutErr)
	}
}

// waitForScanStatus will poll until the fileintegrity that we're looking for reaches a certain status, or until
// a timeout is reached.
func waitForScanStatus(t *testing.T, f *framework.Framework, namespace, name string, targetStatus fileintv1alpha1.FileIntegrityStatusPhase) error {
	return waitForScanStatusWithTimeout(t, f, namespace, name, targetStatus, retryInterval, timeout, 0)
}

// waitForScanStatus will poll until the fileintegrity that we're looking for reaches a certain status for 5
// poll intervals, or until a timeout is reached. The poll interval lets it avoid continuing prematurely if
// the targetStatus is reached from a flapping condition. (i.e., Initializing -> Active -> Initializing where Active is
// seen for a half of a second.
func waitForSuccessiveScanStatus(t *testing.T, f *framework.Framework, namespace, name string, targetStatus fileintv1alpha1.FileIntegrityStatusPhase) error {
	return waitForScanStatusWithTimeout(t, f, namespace, name, targetStatus, retryInterval, timeout, 5)
}

func pollUntilConfigMapDataMatches(t *testing.T, f *framework.Framework, namespace, name, key, expected string, interval, timeout time.Duration) error {
	return wait.PollImmediate(interval, timeout, func() (bool, error) {
		cm, getErr := f.KubeClient.CoreV1().ConfigMaps(namespace).Get(goctx.TODO(), name, metav1.GetOptions{})
		if getErr != nil {
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
		cm, getErr := f.KubeClient.CoreV1().ConfigMaps(namespace).Get(goctx.TODO(), name, metav1.GetOptions{})
		if getErr != nil {
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
	return strings.Contains(content, "/hostroot/etc/resolv.conf")
}

func editFileOnNodes(f *framework.Framework, namespace string) error {
	daemonSet, err := f.KubeClient.AppsV1().DaemonSets(namespace).Create(goctx.TODO(), modifyFileDaemonset(namespace), metav1.CreateOptions{})
	if err != nil {
		return err
	}

	err = waitForDaemonSet(daemonSetExists(f.KubeClient, daemonSet.Name, namespace))
	if err != nil {
		return err
	}
	if err := waitForDaemonSet(daemonSetWasScheduled(f.KubeClient, daemonSet.Name, namespace)); err != nil {
		return err
	}

	time.Sleep(10 * time.Second)
	if err := f.KubeClient.AppsV1().DaemonSets(namespace).Delete(goctx.TODO(), daemonSet.Name, metav1.DeleteOptions{}); err != nil {
		return err
	}
	return nil
}

func addALottaFilesOnNodes(f *framework.Framework, namespace string) error {
	daemonSet, err := f.KubeClient.AppsV1().DaemonSets(namespace).Create(goctx.TODO(), addLotsOfFilesDaemonset(namespace), metav1.CreateOptions{})
	if err != nil {
		return err
	}

	err = waitForDaemonSet(daemonSetExists(f.KubeClient, daemonSet.Name, namespace))
	if err != nil {
		return err
	}
	if err := waitForDaemonSet(daemonSetWasScheduled(f.KubeClient, daemonSet.Name, namespace)); err != nil {
		return err
	}

	time.Sleep(10 * time.Second)
	if err := f.KubeClient.AppsV1().DaemonSets(namespace).Delete(goctx.TODO(), daemonSet.Name, metav1.DeleteOptions{}); err != nil {
		return err
	}
	return nil
}

func cleanNodes(f *framework.Framework, namespace string) error {
	ds, err := f.KubeClient.AppsV1().DaemonSets(namespace).Create(goctx.TODO(), cleanAideDaemonset(namespace), metav1.CreateOptions{})
	if err != nil {
		return err
	}

	err = waitForDaemonSet(daemonSetExists(f.KubeClient, ds.Name, namespace))
	if err != nil {
		return err
	}

	if err := waitForDaemonSet(daemonSetWasScheduled(f.KubeClient, ds.Name, namespace)); err != nil {
		return err
	}

	time.Sleep(10 * time.Second)
	if err := f.KubeClient.AppsV1().DaemonSets(namespace).Delete(goctx.TODO(), ds.Name, metav1.DeleteOptions{}); err != nil {
		return err
	}
	return nil
}

func cleanAddedFilesOnNodes(f *framework.Framework, namespace string) error {
	ds, err := f.KubeClient.AppsV1().DaemonSets(namespace).Create(goctx.TODO(), cleanAideAddedFilesDaemonset(namespace), metav1.CreateOptions{})
	if err != nil {
		return err
	}

	err = waitForDaemonSet(daemonSetExists(f.KubeClient, ds.Name, namespace))
	if err != nil {
		return err
	}

	if err := waitForDaemonSet(daemonSetWasScheduled(f.KubeClient, ds.Name, namespace)); err != nil {
		return err
	}

	time.Sleep(10 * time.Second)
	if err := f.KubeClient.AppsV1().DaemonSets(namespace).Delete(goctx.TODO(), ds.Name, metav1.DeleteOptions{}); err != nil {
		return err
	}
	return nil
}

func getTestMcfg(t *testing.T) *mcfgv1.MachineConfig {
	mode := 420
	trueish := true
	testData := "data:,file-integrity-operator-was-here"
	ignConfig := igntypes.Config{
		Ignition: igntypes.Ignition{
			Version: igntypes.MaxVersion.String(),
		},
		Storage: igntypes.Storage{
			Files: []igntypes.File{
				{
					FileEmbedded1: igntypes.FileEmbedded1{
						Contents: igntypes.Resource{
							Source: &testData,
						},
						Mode: &mode,
					},
					Node: igntypes.Node{
						Path:      "/etc/fi-test-file",
						Overwrite: &trueish,
					},
				},
			},
		},
	}

	rawIgnCfg, _ := json.Marshal(ignConfig)
	mcfg := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "50-" + strings.ToLower(t.Name()),
			Labels: mcLabelForWorkerRole,
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: rawIgnCfg,
			},
		},
	}
	return mcfg
}

func waitForNodesToBeReady(t *testing.T, f *framework.Framework) error {
	nodeList := &corev1.NodeList{}
	// A long time...
	bo := backoff.WithMaxRetries(backoff.NewConstantBackOff(15*time.Second), 360)

	err := backoff.RetryNotify(
		func() error {
			err := f.Client.List(goctx.TODO(), nodeList)
			if err != nil {
				// Returning an error merely makes this retry after the interval
				return err
			}
			for _, node := range nodeList.Items {
				if isNodeReady(node) {
					continue
				}
				return fmt.Errorf("The node '%s' is not ready yet", node.Name)
			}
			return nil
		},
		bo,
		func(err error, d time.Duration) {
			t.Logf("Nodes not ready yet after %s: %s\n", d.String(), err)
		})
	if err != nil {
		return fmt.Errorf("The nodes were never ready: %s", err)
	}
	return nil
}

func isNodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady &&
			condition.Status == corev1.ConditionTrue &&
			node.Annotations[mcfgconst.MachineConfigDaemonStateAnnotationKey] == mcfgconst.MachineConfigDaemonStateDone {
			return true
		}
	}
	return false
}

func assertDSPodHasArg(t *testing.T, f *framework.Framework, fiName, namespace, expectedLine string, interval, timeout time.Duration) error {
	return wait.PollImmediate(interval, timeout, func() (bool, error) {
		ds, getErr := f.KubeClient.AppsV1().DaemonSets(namespace).Get(goctx.TODO(), common.GetDaemonSetName(fiName), metav1.GetOptions{})
		if getErr != nil {
			t.Logf("Retrying. Got error: %v\n", getErr)
			return false, nil
		}
		for _, arg := range ds.Spec.Template.Spec.Containers[0].Args {
			if arg == expectedLine {
				return true, nil
			}
		}
		t.Logf("Expected line not found, retrying")
		return false, nil
	})
}

func getFiDsPods(f *framework.Framework, fileIntegrityName, namespace string) (*corev1.PodList, error) {
	dsName := common.GetDaemonSetName(fileIntegrityName)
	ds, err := f.KubeClient.AppsV1().DaemonSets(namespace).Get(goctx.TODO(), dsName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	lo := metav1.ListOptions{LabelSelector: "app=" + ds.Name}
	pods, err := f.KubeClient.CoreV1().Pods(namespace).List(goctx.TODO(), lo)
	if err != nil {
		return nil, err
	}

	return pods, nil
}

func waitUntilPodsAreGone(t *testing.T, c client.Client, pods *corev1.PodList, interval, timeout time.Duration) error {
	return wait.PollImmediate(interval, timeout, func() (bool, error) {
		for _, pod := range pods.Items {
			var getPod corev1.Pod
			err := c.Get(goctx.TODO(), types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, &getPod)
			if err == nil {
				t.Logf("looping again, pod %s still exists\n", pod.Name)
				return false, nil
			} else if !kerr.IsNotFound(err) {
				return false, err
			}
		}

		t.Log("All previous pods have exited")
		return true, nil
	})
}

func taintNode(t *testing.T, f *framework.Framework, node *corev1.Node, taint corev1.Taint) error {
	taintedNode := node.DeepCopy()
	if taintedNode.Spec.Taints == nil {
		taintedNode.Spec.Taints = []corev1.Taint{}
	}
	taintedNode.Spec.Taints = append(taintedNode.Spec.Taints, taint)
	t.Logf("Tainting node: %s", taintedNode.Name)

	return retryDefault(
		func() error {
			return f.Client.Update(goctx.TODO(), taintedNode)
		},
	)
}

func removeNodeTaint(t *testing.T, f *framework.Framework, nodeName, taintKey string) error {
	taintedNode := &corev1.Node{}
	nodeKey := types.NamespacedName{Name: nodeName}
	if err := f.Client.Get(goctx.TODO(), nodeKey, taintedNode); err != nil {
		t.Logf("Couldn't get node: %s", nodeName)
		return err
	}
	untaintedNode := taintedNode.DeepCopy()
	untaintedNode.Spec.Taints = []corev1.Taint{}
	for _, taint := range taintedNode.Spec.Taints {
		if taint.Key != taintKey {
			untaintedNode.Spec.Taints = append(untaintedNode.Spec.Taints, taint)
		}
	}

	t.Logf("Removing taint from node: %s", nodeName)
	return retryDefault(
		func() error {
			return f.Client.Update(goctx.TODO(), untaintedNode)
		},
	)
}

func retryDefault(operation func() error) error {
	return backoff.Retry(operation, backoff.WithMaxRetries(backoff.NewConstantBackOff(5*time.Second), 5))
}

// getNodesWithSelector lists nodes according to a specific selector
func getNodesWithSelector(f *framework.Framework, labelselector map[string]string) ([]corev1.Node, error) {
	var nodes corev1.NodeList
	lo := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labelselector),
	}
	err := f.Client.List(goctx.TODO(), &nodes, lo)
	return nodes.Items, err
}

func writeToArtifactsDir(dir, scan, pod, container, log string) error {
	logPath := path.Join(dir, fmt.Sprintf("%s_%s_%s.log", scan, pod, container))
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	// #nosec G307
	defer logFile.Close()
	_, err = io.WriteString(logFile, log)
	if err != nil {
		return err
	}
	logFile.Sync()
	return nil
}

func logContainerOutput(t *testing.T, f *framework.Framework, namespace, name string) {
	// Try all container/init variants for each pod and the pod itself (self), log nothing if the container is not applicable.
	containers := []string{"self", "daemon"}
	artifacts := os.Getenv("ARTIFACT_DIR")
	if artifacts == "" {
		return
	}
	pods, err := getFiDsPods(f, name, namespace)
	if err != nil {
		t.Logf("Warning: Error getting pods for container logging: %s", err)
	} else {
		for _, pod := range pods.Items {
			for _, con := range containers {
				logOpts := &corev1.PodLogOptions{}
				if con != "self" {
					logOpts.Container = con
				}
				req := f.KubeClient.CoreV1().Pods(namespace).GetLogs(pod.Name, logOpts)
				podLogs, err := req.Stream(goctx.TODO())
				if err != nil {
					// Silence this error if the container is not valid for the pod
					if !kerr.IsBadRequest(err) {
						t.Logf("error getting logs for %s/%s: reason: %v, err: %v", pod.Name, con, kerr.ReasonForError(err), err)
					}
					continue
				}
				buf := new(bytes.Buffer)
				_, err = io.Copy(buf, podLogs)
				if err != nil {
					t.Logf("error copying logs for %s/%s: %v", pod.Name, con, err)
					continue
				}
				logs := buf.String()
				if len(logs) == 0 {
					t.Logf("no logs for %s/%s", pod.Name, con)
				} else {
					err := writeToArtifactsDir(artifacts, name, pod.Name, con, logs)
					if err != nil {
						t.Logf("error writing logs for %s/%s: %v", pod.Name, con, err)
					} else {
						t.Logf("wrote logs for %s/%s", pod.Name, con)
					}
				}
			}
		}
	}
}

func getMetricResults(t *testing.T) string {
	ocPath, err := exec.LookPath("oc")
	if err != nil {
		t.Fatal(err)
	}
	// We're just under test.
	// G204 (CWE-78): Subprocess launched with variable (Confidence: HIGH, Severity: MEDIUM)
	// #nosec
	cmd := exec.Command(ocPath,
		"run", "--rm", "-i", "--restart=Never", "--image=registry.fedoraproject.org/fedora-minimal:latest",
		"-nopenshift-file-integrity", "metrics-test", "--", "bash", "-c",
		curlFIOCMD,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("error getting output %s", err)
	}
	t.Logf("metrics output:\n%s\n", string(out))
	return string(out)
}

func assertMetric(t *testing.T, content, metric string, expected int) error {
	if val := parseMetric(t, content, metric); val != expected {
		return errors.New(fmt.Sprintf("expected %v for counter %s, got %v", expected, metric, val))
	}
	return nil
}

func assertEachMetric(t *testing.T, expectedMetrics map[string]int) error {
	metricErrs := make([]error, 0)
	metricsOutput := getMetricResults(t)
	for metric, i := range expectedMetrics {
		err := assertMetric(t, metricsOutput, metric, i)
		if err != nil {
			metricErrs = append(metricErrs, err)
		}
	}
	if len(metricErrs) > 0 {
		for err := range metricErrs {
			t.Log(err)
		}
		return errors.New("unexpected metrics value")
	}
	return nil
}

func parseMetric(t *testing.T, content, metric string) int {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, metric) {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				t.Errorf("invalid metric")
			}
			i, err := strconv.Atoi(fields[1])
			if err != nil {
				t.Errorf("invalid metric value")
			}
			return i
		}
	}
	return 0
}
