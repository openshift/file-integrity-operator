package e2e

import (
	goctx "context"
	"strings"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/pkg/errors"

	framework "github.com/operator-framework/operator-sdk/pkg/test"
	"github.com/operator-framework/operator-sdk/pkg/test/e2eutil"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/openshift/file-integrity-operator/pkg/apis"
	fileintv1alpha1 "github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
)

const (
	pollInterval         = time.Second * 2
	retryInterval        = time.Second * 5
	timeout              = time.Minute * 30
	cleanupRetryInterval = time.Second * 1
	cleanupTimeout       = time.Minute * 5
	testIntegrityName    = "test-check"
	testConfName         = "test-conf"
	testConfDataKey      = "conf"
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
!/hostroot/etc/kubernetes/manifests
!/hostroot/etc/docker/certs.d
!/hostroot/etc/selinux/targeted

# Catch everything else in /etc
/hostroot/etc/    CONTENT_EX`

func cleanUp(t *testing.T, namespace string) func() error {
	return func() error {
		f := framework.Global

		list, err := f.KubeClient.CoreV1().ConfigMaps(namespace).List(metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, cm := range list.Items {
			if err := f.KubeClient.CoreV1().ConfigMaps(namespace).Delete(cm.Name, &metav1.DeleteOptions{}); err != nil {
				if !kerr.IsNotFound(err) {
					return err
				}
			}
		}
		return nil
	}
}

func setupTestRequirements(t *testing.T) *framework.TestCtx {
	fileIntegrities := &fileintv1alpha1.FileIntegrityList{}
	err := framework.AddToFrameworkScheme(apis.AddToScheme, fileIntegrities)
	if err != nil {
		t.Fatalf("TEST SETUP: failed to add custom resource scheme to framework: %v", err)
	}
	return framework.NewTestCtx(t)
}
func setupFileIntegrityOperatorCluster(t *testing.T, ctx *framework.TestCtx) {
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
	namespace, err := ctx.GetNamespace()
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

func daemonSetExists(c kubernetes.Interface, name, namespace string) wait.ConditionFunc {
	return func() (bool, error) {
		_, err := c.AppsV1().DaemonSets(namespace).Get(name, metav1.GetOptions{})
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
		daemonSet, err := c.AppsV1().DaemonSets(namespace).Get(name, metav1.GetOptions{})
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
		daemonSet, err := c.AppsV1().DaemonSets(namespace).Get(name, metav1.GetOptions{})
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
					NodeSelector: map[string]string{
						"node-role.kubernetes.io/worker": "",
					},
				},
			},
		},
	}
}

func getDSReplicas(c kubernetes.Interface, name, namespace string) (int, error) {
	daemonSet, err := c.AppsV1().DaemonSets(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		return 0, err
	}
	return int(daemonSet.Status.NumberAvailable), nil
}

func getNumberOfWorkerNodes(c kubernetes.Interface) (int, error) {
	listopts := metav1.ListOptions{
		LabelSelector: "node-role.kubernetes.io/worker",
	}
	nodes, err := c.CoreV1().Nodes().List(listopts)
	if err != nil {
		return 0, err
	}
	return len(nodes.Items), nil
}

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
	return strings.Contains(content, "/hostroot/etc/resolv.conf")
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
