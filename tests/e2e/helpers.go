package e2e

import (
	"bufio"
	"bytes"
	"context"
	goctx "context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	machinev1 "github.com/openshift/api/machine/v1beta1"
	"github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/controller/metrics"
	"github.com/pborman/uuid"

	v1 "k8s.io/api/rbac/v1"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/cenkalti/backoff/v4"
	igntypes "github.com/coreos/ignition/v2/config/v3_2/types"

	"github.com/openshift/file-integrity-operator/tests/e2eutil"
	"github.com/openshift/file-integrity-operator/tests/framework"
	mcfgapi "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgconst "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
	nodeMasterRoleLabelKey  = "node-role.kubernetes.io/master"
	mcWorkerRoleLabelKey    = "machineconfiguration.openshift.io/role"
	defaultTestGracePeriod  = 20
	defaultTestInitialDelay = 0
	testInitialDelay        = 180
	deamonsetWaitTimeout    = 30 * time.Second
	legacyReinitOnHost      = "/hostroot/etc/kubernetes/aide.reinit"
	metricsTestCRBName      = "fio-metrics-client"
	metricsTestSAName       = "default"
	metricsTestTokenName    = "metrics-token"
	machineSetNamespace     = "openshift-machine-api"
	compressionFileCmd      = "for i in `seq 1 10000`; do mktemp \"/hostroot/etc/addedbytest$i.XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX\"; done || true"
)

const (
	curlCMD       = "curl -ks -H \"Authorization: Bearer `cat /var/run/secrets/kubernetes.io/serviceaccount/token`\" "
	metricsURLFmt = "https://metrics.%s.svc:8585/"
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
!/hostroot/root/\.kube
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
!/hostroot/etc/kubernetes/cni/net.d
!/hostroot/etc/kubernetes/cni/net.d/*
!/hostroot/etc/machine-config-daemon/currentconfig$
!/hostroot/etc/pki/ca-trust/extracted/java/cacerts$
!/hostroot/etc/cvo/updatepayloads

# Catch everything else in /etc
/hostroot/etc/    CONTENT_EX`

var brokenAideConfig = testAideConfig + "\n" + "NORMAL = p+i+n+u+g+s+m+c+acl+selinux+xattrs+sha513+md5+XXXXXX"

func newTestFileIntegrity(name, ns string, nodeSelector map[string]string, grace int, debug bool, initialDelay int) *v1alpha1.FileIntegrity {
	return &v1alpha1.FileIntegrity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: v1alpha1.FileIntegritySpec{
			NodeSelector: nodeSelector,
			Config: v1alpha1.FileIntegrityConfig{
				GracePeriod:  grace,
				InitialDelay: initialDelay,
			},
			Debug: debug,
		},
	}
}

func cleanUp(t *testing.T, namespace string) func() error {
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

		if err := deleteMetricsTestResources(f); err != nil {
			return err
		}

		revertManifestFileNamespace(t, f, namespace)

		return deleteStatusEvents(f, namespace)
	}
}

// Revert the test namespace back to a stock value, so subsequent tests deploy properly with a new namespace.
func revertManifestFileNamespace(t *testing.T, f *framework.Framework, namespace string) {
	replaceNamespaceFromManifest(t, namespace, "openshift-file-integrity", f.NamespacedManPath)
}

func deleteMetricsTestResources(f *framework.Framework) error {
	// Delete the metrics test pod's ClusterRoleBinding
	if err := f.KubeClient.RbacV1().ClusterRoleBindings().Delete(goctx.TODO(), metricsTestCRBName, metav1.DeleteOptions{}); err != nil {
		if !kerr.IsNotFound(err) {
			return err
		}
	}
	// Delete the metrics test pod's ClusterRole
	if err := f.KubeClient.RbacV1().ClusterRoles().Delete(goctx.TODO(), metricsTestCRBName, metav1.DeleteOptions{}); err != nil {
		if !kerr.IsNotFound(err) {
			return err
		}
	}
	return nil
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
	fileIntegrities := &v1alpha1.FileIntegrityList{}
	nodeStatus := &v1alpha1.FileIntegrityNodeStatusList{}

	err := framework.AddToFrameworkScheme(v1alpha1.AddToScheme, fileIntegrities)
	if err != nil {
		t.Fatalf("TEST SETUP: failed to add custom resource scheme to framework: %v", err)
	}

	mcList := &mcfgv1.MachineConfigList{}
	err = framework.AddToFrameworkScheme(mcfgapi.Install, mcList)
	if err != nil {
		t.Fatalf("TEST SETUP: failed to add custom resource scheme to framework: %v", err)
	}

	err = framework.AddToFrameworkScheme(v1alpha1.AddToScheme, nodeStatus)
	if err != nil {
		t.Fatalf("TEST SETUP: failed to add custom resource scheme to framework: %v", err)
	}
	return framework.NewContext(t)
}

func replaceNamespaceFromManifest(t *testing.T, nsFrom, nsTo string, namespacedManPath *string) {
	if namespacedManPath == nil {
		t.Fatal("Error: no namespaced manifest given as test argument. operator-sdk might have changed.")
	}
	manPath := *namespacedManPath
	// #nosec
	read, err := ioutil.ReadFile(manPath)
	if err != nil {
		t.Fatalf("Error reading namespaced manifest file: %s", err)
	}

	newContents := strings.Replace(string(read), nsFrom, nsTo, -1)

	// #nosec
	err = ioutil.WriteFile(manPath, []byte(newContents), 644)
	if err != nil {
		t.Fatalf("Error writing namespaced manifest file: %s", err)
	}
}

func setupFileIntegrityOperatorCluster(t *testing.T, ctx *framework.Context) {
	cleanupOptions := framework.CleanupOptions{
		TestContext:   ctx,
		Timeout:       cleanupTimeout,
		RetryInterval: cleanupRetryInterval,
	}

	// get global framework variables
	f := framework.Global
	namespace, err := ctx.GetOperatorNamespace()
	if err != nil {
		t.Fatal(err)
	}

	replaceNamespaceFromManifest(t, "openshift-file-integrity", namespace, f.NamespacedManPath)

	if _, ok := os.LookupEnv(framework.TestBundleInstallEnv); !ok {
		err = ctx.InitializeClusterResources(&cleanupOptions)
		if err != nil {
			t.Fatalf("Failed to initialize cluster resources: %v", err)
		}
		t.Log("Initialized cluster resources")
	} else {
		t.Logf("Using existing cluster resources in namespace %s", namespace)
	}

	err = initializeMetricsTestResources(f, namespace)
	if err != nil {
		t.Fatalf("Failed to initialize cluster resources for metrics: %v", err)
	}

	// wait for file-integrity-operator to be ready
	err = e2eutil.WaitForOperatorDeployment(t, f.KubeClient, namespace, "file-integrity-operator", 1, retryInterval, timeout)
	if err != nil {
		t.Fatal(err)
	}
}

// Initializes the permission resources needed for the in-test metrics scraping
func initializeMetricsTestResources(f *framework.Framework, namespace string) error {
	if _, err := f.KubeClient.RbacV1().ClusterRoles().Create(goctx.TODO(), &v1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: metricsTestCRBName,
		},
		Rules: []v1.PolicyRule{
			{
				NonResourceURLs: []string{
					metrics.HandlerPath,
				},
				Verbs: []string{
					"get",
				},
			},
		},
	}, metav1.CreateOptions{}); err != nil && !kerr.IsAlreadyExists(err) {
		return err
	}

	if _, err := f.KubeClient.RbacV1().ClusterRoleBindings().Create(goctx.TODO(), &v1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: metricsTestCRBName,
		},
		Subjects: []v1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      metricsTestSAName,
				Namespace: namespace,
			},
		},
		RoleRef: v1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     metricsTestCRBName,
		},
	}, metav1.CreateOptions{}); err != nil && !kerr.IsAlreadyExists(err) {
		return err
	}

	if _, err := f.KubeClient.CoreV1().Secrets(namespace).Create(goctx.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      metricsTestTokenName,
			Namespace: namespace,
			Annotations: map[string]string{
				"kubernetes.io/service-account.name": metricsTestSAName,
			},
		},
		Type: "kubernetes.io/service-account-token",
	}, metav1.CreateOptions{}); err != nil && !kerr.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// setupTest deploys the operator
func setupTest(t *testing.T) (*framework.Framework, *framework.Context, string) {
	testctx := setupTestRequirements(t)
	namespace, err := testctx.GetOperatorNamespace()
	if err != nil {
		t.Fatalf("could not get namespace: %v", err)
	}
	testctx.AddCleanupFn(cleanUp(t, namespace))

	setupFileIntegrityOperatorCluster(t, testctx)
	return framework.Global, testctx, namespace
}

// setupFileIntegrity creates the FileIntegrity instance with the default grace period.
func setupFileIntegrity(t *testing.T, f *framework.Framework, testCtx *framework.Context, integrityName, namespace string, nodeSelectorKey string, gracePeriod int) {
	var testIntegrityCheck *v1alpha1.FileIntegrity
	var nodeSelector map[string]string
	if gracePeriod == 0 {
		gracePeriod = defaultTestGracePeriod
	}

	if nodeSelectorKey != "" {
		nodeSelector = map[string]string{
			nodeSelectorKey: "",
		}
	}

	testIntegrityCheck = newTestFileIntegrity(integrityName, namespace, nodeSelector, gracePeriod, true, defaultTestInitialDelay)

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

	dsName := common.DaemonSetName(testIntegrityCheck.Name)
	err = waitForDaemonSet(daemonSetIsReady(f.KubeClient, dsName, namespace))
	if err != nil {
		t.Fatalf("Timed out waiting for DaemonSet %s", dsName)
	}

	var lastErr error

	pollErr := wait.PollImmediate(time.Second, 5*time.Minute, func() (bool, error) {
		numReplicas, err := getDSReplicas(f.KubeClient, dsName, namespace)
		if err != nil {
			lastErr = err
			return false, nil
		}

		numOfSelectedNodes, err := getNumberOfNodesFromSelector(f.KubeClient, nodeSelectorKey)
		if err != nil {
			lastErr = err
			return false, nil
		}
		if numOfSelectedNodes != numReplicas {
			lastErr = errors.Errorf("The number of selected nodes with label %s (%d) doesn't match the DS replicas (%d)", nodeSelectorKey, numOfSelectedNodes, numReplicas)
			return false, nil
		}
		return true, nil
	})
	if pollErr != nil {
		t.Fatalf("Error confirming DS replica amount: (%v) (%v)", pollErr, lastErr)
	}

	// wait to go active
	err = waitForScanStatus(t, f, namespace, integrityName, v1alpha1.PhaseActive)
	if err != nil {
		t.Fatalf("Timed out waiting for scan status to go Active")
	}
	t.Log("FileIntegrity deployed successfully")
}

// setupFileIntegrityWithInitialDelay creates the FileIntegrity instance with the default grace period and an initial delay.
func setupFileIntegrityWithInitialDelay(t *testing.T, f *framework.Framework, testCtx *framework.Context, integrityName, namespace string) {
	testIntegrityCheck := newTestFileIntegrity(integrityName, namespace, nodeLabelForWorkerRole, defaultTestGracePeriod, true, testInitialDelay)

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

func waitForDaemonSetTimeout(daemonSetCallback wait.ConditionFunc, timeoutVal time.Duration) error {
	return wait.PollImmediate(pollInterval, timeoutVal, daemonSetCallback)
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

// This pod is to stuff the aide log with added files, to force compression. The trick is in the long temp name that pads the aide log.
// 10000 files.
func addCompressionTestFilesOnNode(t *testing.T, f *framework.Framework, ctx *framework.Context, node, namespace string) error {
	pod := privPodOnNode(namespace, "test-compression-pod", node, compressionFileCmd)
	if err := f.Client.Create(goctx.TODO(), pod, &framework.CleanupOptions{
		TestContext:   ctx,
		Timeout:       cleanupTimeout,
		RetryInterval: cleanupRetryInterval,
	}); err != nil {
		return err
	}

	// We expect the command to finish and return 0.
	return waitForPod(containerCompleted(t, f.KubeClient, pod.Name, namespace, 0))
}

func privPodOnNode(namespace, name, nodeName, command string) *corev1.Pod {
	priv := true
	runAs := int64(0)

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: "Never",
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
					Image:   "quay.io/prometheus/busybox",
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
				corev1.LabelHostname: nodeName,
			},
		},
	}
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
							Image:   "quay.io/prometheus/busybox",
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

func getNumberOfNodesFromSelector(c kubernetes.Interface, nodeSelectorKey string) (int, error) {
	listopts := metav1.ListOptions{}
	if nodeSelectorKey != "" {
		listopts = metav1.ListOptions{
			LabelSelector: nodeSelectorKey,
		}
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
	testctx.AddCleanupFn(cleanUp(t, namespace))
	setupFileIntegrityOperatorCluster(t, testctx)

	if err := taintNode(t, f, taintedNode, taint); err != nil {
		t.Fatal("Tainting node failed")
	}

	t.Log("Creating FileIntegrity object for Toleration tests")
	testIntegrityCheck := &v1alpha1.FileIntegrity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      integrityName,
			Namespace: namespace,
		},
		Spec: v1alpha1.FileIntegritySpec{
			NodeSelector: map[string]string{
				// Schedule on the tainted host
				corev1.LabelHostname: taintedNode.Labels[corev1.LabelHostname],
			},
			Config: v1alpha1.FileIntegrityConfig{
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

	dsName := common.DaemonSetName(testIntegrityCheck.Name)
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
		fileIntegrity := &v1alpha1.FileIntegrity{}
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

func removeFileIntegrityConfigMapLabel(t *testing.T, f *framework.Framework, integrityName, namespace string) {
	var lastErr error
	pollErr := wait.PollImmediate(time.Second, 2*time.Minute, func() (bool, error) {
		confMap := &corev1.ConfigMap{}
		err := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: integrityName, Namespace: namespace}, confMap)
		if err != nil {
			lastErr = err
			return false, nil
		}

		// Remove the label, keep the aide-conf tag.
		confMapCopy := confMap.DeepCopy()
		confMapCopy.Labels = nil
		confMapCopy.Labels = map[string]string{
			"file-integrity.openshift.io/aide-conf": "",
		}

		err = f.Client.Update(goctx.TODO(), confMapCopy)
		if err != nil {
			lastErr = err
			return false, nil
		}
		return true, nil
	})
	if pollErr != nil {
		t.Errorf("Error updating configMap with a nil label: (%s) (%s)", pollErr, lastErr)
	}
}

func reinitFileIntegrityDatabase(t *testing.T, f *framework.Framework, integrityName, namespace string, interval, timeout time.Duration) {
	var lastErr error
	pollErr := wait.PollImmediate(interval, timeout, func() (bool, error) {
		fileIntegrity := &v1alpha1.FileIntegrity{}
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

func waitForScanStatusWithTimeout(t *testing.T, f *framework.Framework, namespace, name string, targetStatus v1alpha1.FileIntegrityStatusPhase, interval, timeout time.Duration, successiveResults int) error {
	exampleFileIntegrity := &v1alpha1.FileIntegrity{}
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

func waitForFailedResultForNode(t *testing.T, f *framework.Framework, namespace, name, node string, interval, timeout time.Duration) (*v1alpha1.FileIntegrityScanResult, error) {
	var foundResult *v1alpha1.FileIntegrityScanResult
	// retry and ignore errors until timeout
	err := wait.Poll(interval, timeout, func() (bool, error) {
		nodeStatus := &v1alpha1.FileIntegrityNodeStatus{}
		getErr := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: name + "-" + node, Namespace: namespace}, nodeStatus)
		if getErr != nil {
			return false, nil
		}

		if nodeStatus.LastResult.Condition == v1alpha1.NodeConditionFailed {
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
	exampleFileIntegrity := &v1alpha1.FileIntegrity{}
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

func scaleUpWorkerMachineSet(t *testing.T, f *framework.Framework, interval, timeout time.Duration) (string, string) {
	// Add a new worker node to the cluster through the machineset
	// Get the machineset
	machineSets := &machinev1.MachineSetList{}
	err := f.Client.List(context.TODO(), machineSets, &client.ListOptions{
		Namespace: machineSetNamespace})
	if err != nil {
		t.Error(err)
	}
	if len(machineSets.Items) == 0 {
		t.Error("No machinesets found")
	}
	machineSetName := ""
	for _, ms := range machineSets.Items {
		if ms.Spec.Replicas != nil && *ms.Spec.Replicas > 0 {
			t.Logf("Found machineset %s with %d replicas", ms.Name, *ms.Spec.Replicas)
			machineSetName = ms.Name
			break
		}
	}

	// Add one more replica to one of the machinesets
	machineSet := &machinev1.MachineSet{}
	err = f.Client.Get(context.TODO(), types.NamespacedName{Name: machineSetName, Namespace: machineSetNamespace}, machineSet)
	if err != nil {
		t.Error(err)
	}
	t.Logf("Scaling up machineset %s", machineSetName)

	replicas := *machineSet.Spec.Replicas + 1
	machineSet.Spec.Replicas = &replicas
	err = f.Client.Update(context.TODO(), machineSet)
	if err != nil {
		t.Error(err)
	}
	t.Logf("Waiting for scaling up machineset %s", machineSetName)
	provisionningMachineName := ""
	err = wait.Poll(interval, timeout, func() (bool, error) {
		err = f.Client.Get(context.TODO(), types.NamespacedName{Name: machineSetName, Namespace: machineSetNamespace}, machineSet)
		if err != nil {
			t.Error(err)
		}
		// get name of the new machine
		if provisionningMachineName == "" {
			machines := &machinev1.MachineList{}
			err = f.Client.List(context.TODO(), machines, &client.ListOptions{
				Namespace: machineSetNamespace})
			if err != nil {
				t.Error(err)
			}
			for _, machine := range machines.Items {
				if *machine.Status.Phase == "Provisioning" {
					provisionningMachineName = machine.Name
					break
				}
			}
		}
		if machineSet.Status.Replicas == machineSet.Status.ReadyReplicas {
			t.Logf("Machineset %s scaled up", machineSetName)
			return true, nil
		}
		t.Logf("Waiting for machineset %s to scale up, current ready replicas: %d of %d", machineSetName, machineSet.Status.ReadyReplicas, machineSet.Status.Replicas)
		return false, nil
	})
	if err != nil {
		t.Error(err)
	}
	// get the new node name
	newNodeName := ""
	machine := &machinev1.Machine{}
	err = f.Client.Get(context.TODO(), types.NamespacedName{Name: provisionningMachineName, Namespace: machineSetNamespace}, machine)
	if err != nil {
		t.Error(err)
	}
	newNodeName = machine.Status.NodeRef.Name
	t.Logf("New node name is %s", newNodeName)

	return machineSetName, newNodeName
}

func scaleDownWorkerMachineSet(t *testing.T, f *framework.Framework, machineSetName string, interval, timeout time.Duration) string {
	// Remove the worker node from the cluster through the machineset
	// Get the machineset
	machineSet := &machinev1.MachineSet{}
	err := f.Client.Get(context.TODO(), types.NamespacedName{Name: machineSetName, Namespace: machineSetNamespace}, machineSet)
	if err != nil {
		t.Error(err)
	}

	// Remove one replica from the machineset
	t.Logf("Scaling down machineset %s", machineSetName)
	replicas := *machineSet.Spec.Replicas - 1
	machineSet.Spec.Replicas = &replicas
	err = f.Client.Update(context.TODO(), machineSet)
	if err != nil {
		t.Error(err)
	}
	deletedNodeName := ""
	t.Logf("Waiting for scaling down machineset %s", machineSetName)
	err = wait.Poll(interval, timeout, func() (bool, error) {
		err = f.Client.Get(context.TODO(), types.NamespacedName{Name: machineSetName, Namespace: machineSetNamespace}, machineSet)
		if err != nil {
			t.Error(err)
		}
		if machineSet.Status.Replicas == machineSet.Status.ReadyReplicas {
			t.Logf("Machineset %s scaled down", machineSetName)
			return true, nil
		}
		t.Logf("Waiting for machineset %s to scale down, current ready replicas: %d of %d", machineSet.Name, machineSet.Status.ReadyReplicas, machineSet.Status.Replicas)
		return false, nil
	})
	if err != nil {
		t.Error(err)
	}
	if deletedNodeName == "" {
		// Get the node that was deleted
		machineList := &machinev1.MachineList{}
		err = f.Client.List(context.TODO(), machineList, &client.ListOptions{
			Namespace: machineSetNamespace})
		if err != nil {
			t.Error(err)
		}
		if len(machineList.Items) == 0 {
			t.Error("No machines found")
		}
		for _, machine := range machineList.Items {
			if machine.DeletionTimestamp != nil {
				deletedNodeName = machine.Status.NodeRef.Name
				t.Logf("Found deleted node %s", deletedNodeName)
				return deletedNodeName
			}
		}
	}
	return deletedNodeName
}

func assertNodeStatusForRemovedNode(t *testing.T, f *framework.Framework, integrityName, namespace, deletedNodeName string, interval, timeout time.Duration) {
	timeoutErr := wait.PollImmediate(interval, timeout, func() (bool, error) {
		nodestatus := &v1alpha1.FileIntegrityNodeStatus{}
		err := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: integrityName + "-" + deletedNodeName, Namespace: namespace}, nodestatus)
		if err != nil {
			if kerr.IsNotFound(err) {
				t.Logf("Node status for node %s not found, as expected", deletedNodeName)
				return true, nil
			} else {
				t.Errorf("error getting node status for node %s: %v", deletedNodeName, err)
				return true, err
			}
		} else {
			t.Logf("Node status for node %s found, waiting for it to be deleted", deletedNodeName)
			return false, nil
		}
	})
	if timeoutErr != nil {
		t.Errorf("timed out waiting for node status for node %s to be deleted", deletedNodeName)
	}
}

func assertNodesConditionIsSuccess(t *testing.T, f *framework.Framework, integrityName, namespace string, interval, timeout time.Duration, nodeLabelKey string) {
	var lastErr error
	type nodeStatus struct {
		LastProbeTime metav1.Time
		Condition     v1alpha1.FileIntegrityNodeCondition
	}

	listopts := metav1.ListOptions{}
	if nodeLabelKey != "" {
		listopts.LabelSelector = nodeLabelKey
	}

	nodes, err := f.KubeClient.CoreV1().Nodes().List(goctx.TODO(), listopts)
	if err != nil {
		t.Errorf("error listing nodes: %v", err)
	}
	timeoutErr := wait.PollImmediate(interval, timeout, func() (bool, error) {
		// Node names are the key
		latestStatuses := map[string]nodeStatus{}
		for _, node := range nodes.Items {
			fileIntegNodeStatus := &v1alpha1.FileIntegrityNodeStatus{}
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
			if status.Condition != v1alpha1.NodeConditionSucceeded {
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
		Condition     v1alpha1.FileIntegrityNodeCondition
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
			fileIntegNodeStatus := &v1alpha1.FileIntegrityNodeStatus{}
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
			if status.Condition != v1alpha1.NodeConditionSucceeded {
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
func waitForScanStatus(t *testing.T, f *framework.Framework, namespace, name string, targetStatus v1alpha1.FileIntegrityStatusPhase) error {
	return waitForScanStatusWithTimeout(t, f, namespace, name, targetStatus, retryInterval, timeout, 0)
}

// waitForScanStatus will poll until the fileintegrity that we're looking for reaches a certain status for 5
// poll intervals, or until a timeout is reached. The poll interval lets it avoid continuing prematurely if
// the targetStatus is reached from a flapping condition. (i.e., Initializing -> Active -> Initializing where Active is
// seen for a half of a second.
func waitForSuccessiveScanStatus(t *testing.T, f *framework.Framework, namespace, name string, targetStatus v1alpha1.FileIntegrityStatusPhase) error {
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

func pollUntilConfigMapHasLabel(t *testing.T, f *framework.Framework, namespace, name, labelName string, interval, timeout time.Duration) error {
	return wait.PollImmediate(interval, timeout, func() (bool, error) {
		cm, getErr := f.KubeClient.CoreV1().ConfigMaps(namespace).Get(goctx.TODO(), name, metav1.GetOptions{})
		if getErr != nil {
			return false, nil
		}
		_, ok := cm.Labels[labelName]
		if !ok {
			return false, nil
		}
		return true, nil
	})
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

func waitForPod(podCallback wait.ConditionFunc) error {
	return wait.PollImmediate(retryInterval, timeout, podCallback)
}

// containerCompleted returns a ConditionFunc that passes if all containers have succeeded and match the exit code.
func containerCompleted(t *testing.T, c kubernetes.Interface, name, namespace string, matchExit int32) wait.ConditionFunc {
	return func() (bool, error) {
		pod, err := c.CoreV1().Pods(namespace).Get(goctx.TODO(), name, metav1.GetOptions{})
		if err != nil && !kerr.IsNotFound(err) {
			return false, err
		}
		if kerr.IsNotFound(err) {
			t.Logf("Pod %s not found yet", name)
			return false, nil
		}

		for _, podStatus := range pod.Status.ContainerStatuses {
			t.Log(podStatus)

			// the container must have terminated
			if podStatus.State.Terminated == nil {
				t.Log("container did not terminate yet")
				return false, nil
			}

			if podStatus.State.Terminated.ExitCode != matchExit {
				return true, errors.New(fmt.Sprintf("the expected exit code did not match %d", matchExit))
			} else {
				t.Logf("container in pod %s has finished", name)
				return true, nil
			}
		}

		t.Logf("container in pod %s not finished yet", name)
		return false, nil
	}
}

func createLegacyReinitFile(t *testing.T, ctx *framework.Context, f *framework.Framework, node, namespace string) error {
	pod := privPodOnNode(namespace, "test-touch-pod", node, "touch "+legacyReinitOnHost+" || true")
	if err := f.Client.Create(goctx.TODO(), pod, &framework.CleanupOptions{
		TestContext:   ctx,
		Timeout:       cleanupTimeout,
		RetryInterval: cleanupRetryInterval,
	}); err != nil {
		return err
	}

	return waitForPod(containerCompleted(t, f.KubeClient, pod.Name, namespace, 0))
}

func waitForLegacyReinitConfirm(t *testing.T, ctx *framework.Context, f *framework.Framework, node, namespace string) error {
	return wait.PollImmediate(5*time.Second, 5*time.Minute, func() (bool, error) {
		err := confirmRemovedLegacyReinitFile(t, ctx, f, node, namespace)
		if err == nil {
			return true, nil
		}
		t.Logf("legacy reinit file not removed yet, trying again")
		return false, nil
	})
}

func confirmRemovedLegacyReinitFile(t *testing.T, ctx *framework.Context, f *framework.Framework, node, namespace string) error {
	pod := privPodOnNode(namespace, "ls"+"-"+uuid.NewRandom().String(), node, "ls "+legacyReinitOnHost)
	if err := f.Client.Create(goctx.TODO(), pod, &framework.CleanupOptions{
		TestContext:   ctx,
		Timeout:       cleanupTimeout,
		RetryInterval: cleanupRetryInterval,
	}); err != nil {
		return err
	}

	// We expect the ls command to exit 1 when the legacy reinit file is missing
	return waitForPod(containerCompleted(t, f.KubeClient, pod.Name, namespace, 1))
}

// tests for a set of re-init backups when MaxBackups is set to 1.
func confirmMaxBackupFiles(t *testing.T, ctx *framework.Context, f *framework.Framework, node, namespace string) error {
	pod := privPodOnNode(namespace, "test-backup-pod", node, "test `ls -1 /hostroot/etc/kubernetes/aide.db.gz.backup-* | wc -l` -eq 1")
	if err := f.Client.Create(goctx.TODO(), pod, &framework.CleanupOptions{
		TestContext:   ctx,
		Timeout:       cleanupTimeout,
		RetryInterval: cleanupRetryInterval,
	}); err != nil {
		return err
	}

	// We expect the test command to return 0 when there's only one aide.db.gz backup
	return waitForPod(containerCompleted(t, f.KubeClient, pod.Name, namespace, 0))
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
			time.Sleep(30 * time.Second) // wait for MCO to start updating other nodes
			return true
		}
	}
	return false
}

func assertMasterDSNoRestart(t *testing.T, f *framework.Framework, fiName, namespace string) {
	pods, err := getFiDsPods(f, fiName, namespace)
	if err != nil {
		t.Errorf("Failed to get daemonset pods: %s", err)
	}

	// get master node list
	masterNodes, err := f.KubeClient.CoreV1().Nodes().List(goctx.TODO(), metav1.ListOptions{LabelSelector: nodeMasterRoleLabelKey})
	if err != nil {
		t.Errorf("Failed to get master nodes: %s", err)
	}
	// get master node name list
	masterNodeNames := map[string]string{}
	for _, node := range masterNodes.Items {
		masterNodeNames[node.Name] = ""
	}

	for _, pod := range pods.Items {
		// check if pod is on master node
		if _, ok := masterNodeNames[pod.Spec.NodeName]; ok {
			// check if pod has restarted
			if pod.Status.ContainerStatuses[0].RestartCount > 0 {
				t.Errorf("Master node pod %s has restarted", pod.Name)
			}
		}
	}
	t.Logf("Master node pods have not restarted")
}

func assertDSPodHasArg(t *testing.T, f *framework.Framework, fiName, namespace, expectedLine string, interval, timeout time.Duration) error {
	return wait.PollImmediate(interval, timeout, func() (bool, error) {
		ds, getErr := f.KubeClient.AppsV1().DaemonSets(namespace).Get(goctx.TODO(), common.DaemonSetName(fiName), metav1.GetOptions{})
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
	dsName := common.DaemonSetName(fileIntegrityName)
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

// Kills the operator pods to reset metrics during a bundle-install test.
func resetBundleTestMetrics(f *framework.Framework, namespace string) error {
	// Kill the operator pod during a continuous run to reset metrics.
	if _, ok := os.LookupEnv(framework.TestBundleInstallEnv); ok {
		if err := killOperatorPods(f, namespace); err != nil {
			return err
		}
	}
	return nil
}

func killOperatorPods(f *framework.Framework, namespace string) error {
	pods, err := getFiOperatorPods(f, namespace)
	if err != nil && !kerr.IsNotFound(err) {
		return err
	}

	if pods != nil {
		for _, p := range pods.Items {
			if err := f.KubeClient.CoreV1().Pods(p.Namespace).Delete(goctx.TODO(), p.Name, metav1.DeleteOptions{}); err != nil {
				return err
			}
		}
	}
	return nil
}

func getFiOperatorPods(f *framework.Framework, namespace string) (*corev1.PodList, error) {
	lo := metav1.ListOptions{LabelSelector: "name=file-integrity-operator"}
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
		return
	}
	operatorPods, err := getFiOperatorPods(f, namespace)
	if err != nil {
		t.Logf("Warning: Error getting pods for container logging: %s", err)
		return
	}

	pods.Items = append(operatorPods.Items)

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

func getMetricResults(t *testing.T, namespace string) string {
	out := runOCandGetOutput(t, []string{
		"run", "--rm", "-i", "--restart=Never", "--image=registry.fedoraproject.org/fedora-minimal:latest",
		"-n" + namespace, "metrics-test", "--", "bash", "-c",
		getCurlFIOCMD(namespace),
	})

	t.Logf("metrics output:\n%s\n", out)
	return out
}

func getCurlFIOCMD(namespace string) string {
	return curlCMD + fmt.Sprintf(metricsURLFmt, namespace) + "metrics-fio"
}

func getOCpath(t *testing.T) string {
	ocPath, err := exec.LookPath("oc")
	if err != nil {
		t.Fatal(err)
	}
	return ocPath
}

func runOCandGetOutput(t *testing.T, arg []string) string {
	ocPath := getOCpath(t)

	// We're just under test.
	// G204 (CWE-78): Subprocess launched with variable (Confidence: HIGH, Severity: MEDIUM)
	// #nosec
	t.Logf("command: %s\n", arg)
	cmd := exec.Command(ocPath, arg...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("error getting output %s", err)
	}
	return string(out)
}

func assertMetric(t *testing.T, content, metric string, expected int) error {
	if val := parseMetric(t, content, metric); val != expected {
		return errors.New(fmt.Sprintf("expected %v for counter %s, got %v", expected, metric, val))
	}
	return nil
}

func assertEachMetric(t *testing.T, namespace string, expectedMetrics map[string]int) error {
	metricErrs := make([]error, 0)
	metricsOutput := getMetricResults(t, namespace)
	for metric, i := range expectedMetrics {
		err := assertMetric(t, metricsOutput, metric, i)
		if err != nil {
			metricErrs = append(metricErrs, err)
		}
	}
	if len(metricErrs) > 0 {
		for _, err := range metricErrs {
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
