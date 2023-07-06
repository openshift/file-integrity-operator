package framework

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/controller/metrics"
	mcfgapi "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	cleanupRetryInterval = time.Second * 1
	cleanupTimeout       = time.Minute * 5
	metricsTestCRBName   = "fio-metrics-client"
	metricsTestSAName    = "default"
	metricsTestTokenName = "metrics-token"
	retryInterval        = time.Second * 5
	timeout              = time.Minute * 30
)

func MainEntry(m *testing.M) {
	f, err := NewFramework()
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	Global = f

	exitCode, err := f.runM(m)
	if err != nil {
		log.Fatal(err)
	}
	os.Exit(exitCode)
}

func NewFramework() (*Framework, error) {
	fopts := &frameworkOpts{}
	fopts.addToFlagSet(flag.CommandLine)

	kcFlag := flag.Lookup(KubeConfigFlag)
	if kcFlag == nil {
		flag.StringVar(&fopts.kubeconfigPath, KubeConfigFlag, "", "path to kubeconfig")
	}

	flag.Parse()

	if kcFlag != nil {
		fopts.kubeconfigPath = kcFlag.Value.String()
	}

	f, err := newFramework(fopts)
	if err != nil {
		log.Fatalf("Failed to create framework: %v", err)
	}

	Global = f
	return f, nil
}

func (f *Framework) SetUp() error {
	// setup context to use when setting up crd
	ctx := NewSuiteContext()
	defer ctx.Cleanup()
	ns, err := ctx.GetOperatorNamespace()
	if err != nil {
		return fmt.Errorf("TEST SETUP: failed to create or get namespace %s for testing: %v", ns, err)
	}
	log.Infof("Created namespace %s", f.OperatorNamespace)
	log.Infof("Created namespace %s", ns)

	// go test always runs from the test directory; change to project root
	err = os.Chdir(f.projectRoot)
	if err != nil {
		return fmt.Errorf("failed to change directory to project root: %w", err)
	}

	// create crd
	globalYAML, err := ioutil.ReadFile(f.globalManPath)
	if err != nil {
		return fmt.Errorf("failed to read global resource manifest: %w", err)
	}
	err = ctx.createFromYAML(globalYAML, true, &CleanupOptions{TestContext: ctx})
	if err != nil {
		return fmt.Errorf("failed to create resource(s) in global resource manifest: %w", err)
	}

	//
	fileIntegrities := &v1alpha1.FileIntegrityList{}
	nodeStatus := &v1alpha1.FileIntegrityNodeStatusList{}

	err = AddToFrameworkScheme(v1alpha1.AddToScheme, fileIntegrities)
	if err != nil {
		return fmt.Errorf("TEST SETUP: failed to add custom resource scheme to framework: %v", err)
	}

	mcList := &mcfgv1.MachineConfigList{}
	err = AddToFrameworkScheme(mcfgapi.Install, mcList)
	if err != nil {
		return fmt.Errorf("TEST SETUP: failed to add custom resource scheme to framework: %v", err)
	}

	err = AddToFrameworkScheme(v1alpha1.AddToScheme, nodeStatus)
	if err != nil {
		return fmt.Errorf("TEST SETUP: failed to add custom resource scheme to framework: %v", err)
	}

	// This is not used in CI, and it's unclear how many contributors use
	// this for local development.
	if f.LocalOperator {
		if err := f.startOperatorLocally(); err != nil {
			return err
		}
	}
	replaceNamespaceFromManifest("openshift-file-integrity", f.OperatorNamespace, f.NamespacedManPath)

	co := CleanupOptions{
		TestContext:   ctx,
		Timeout:       cleanupTimeout,
		RetryInterval: cleanupRetryInterval,
	}
	if _, ok := os.LookupEnv(TestBundleInstallEnv); !ok {
		err = ctx.InitializeClusterResources(&co)
		if err != nil {
			return fmt.Errorf("Failed to initialize cluster resources: %v", err)
		}
		log.Info("Initialized cluster resources")
	} else {
		log.Infof("Using existing cluster resources in namespace %s", f.OperatorNamespace)
	}

	err = f.initializeMetricsTestResources()
	if err != nil {
		return fmt.Errorf("Failed to initialize cluster resources for metrics: %v", err)
	}

	err = f.waitForDeployment("file-integrity-operator", 1, retryInterval, timeout)
	if err != nil {
		return err
	}
	return nil
}

func (f *Framework) startOperatorLocally() error {
	outBuf := &bytes.Buffer{}
	localCmd, err := f.setupLocalCommand()
	if err != nil {
		return fmt.Errorf("failed to setup local command: %w", err)
	}
	localCmd.Stdout = outBuf
	localCmd.Stderr = outBuf

	err = localCmd.Start()
	if err != nil {
		return fmt.Errorf("failed to run operator locally: %w", err)
	}
	log.Info("Started local operator")
	return nil
}

// Initializes the permission resources needed for the in-test metrics scraping
func (f *Framework) initializeMetricsTestResources() error {
	if _, err := f.KubeClient.RbacV1().ClusterRoles().Create(context.TODO(), &v1.ClusterRole{
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

	if _, err := f.KubeClient.RbacV1().ClusterRoleBindings().Create(context.TODO(), &v1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: metricsTestCRBName,
		},
		Subjects: []v1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      metricsTestSAName,
				Namespace: f.OperatorNamespace,
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

	if _, err := f.KubeClient.CoreV1().Secrets(f.OperatorNamespace).Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      metricsTestTokenName,
			Namespace: f.OperatorNamespace,
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

func (f *Framework) waitForDeployment(name string, replicas int, retryInterval, timeout time.Duration) error {
	if f.LocalOperator {
		log.Info("Operator is running locally; skip waitForDeployment")
		return nil
	}
	err := wait.Poll(retryInterval, timeout, func() (done bool, err error) {
		deployment, err := f.KubeClient.AppsV1().Deployments(f.OperatorNamespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				log.Infof("Waiting for availability of Deployment: %s in Namespace: %s \n", name, f.OperatorNamespace)
				return false, nil
			}
			return false, err
		}

		if int(deployment.Status.AvailableReplicas) >= replicas {
			return true, nil
		}
		log.Infof("Waiting for full availability of %s deployment (%d/%d)\n", name,
			deployment.Status.AvailableReplicas, replicas)
		return false, nil
	})
	if err != nil {
		return err
	}
	log.Infof("Deployment available (%d/%d)\n", replicas, replicas)
	return nil
}

func (f *Framework) TearDown() error {
	if err := f.deleteConfigMaps(); err != nil {
		return err
	}
	if err := f.deleteMetricsTestResources(); err != nil {
		return err
	}
	if err := f.revertManifestFileNamespace(); err != nil {
		return err
	}
	if err := f.deleteStatusEvents(); err != nil {
		return err
	}
	return nil
}

func (f *Framework) deleteConfigMaps() error {
	list, err := f.KubeClient.CoreV1().ConfigMaps(f.OperatorNamespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, cm := range list.Items {
		if err := f.KubeClient.CoreV1().ConfigMaps(f.OperatorNamespace).Delete(context.TODO(), cm.Name, metav1.DeleteOptions{}); err != nil {
			if !kerr.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func (f *Framework) deleteMetricsTestResources() error {
	// Delete the metrics test pod's ClusterRoleBinding
	if err := f.KubeClient.RbacV1().ClusterRoleBindings().Delete(context.TODO(), metricsTestCRBName, metav1.DeleteOptions{}); err != nil {
		if !kerr.IsNotFound(err) {
			return err
		}
	}
	// Delete the metrics test pod's ClusterRole
	if err := f.KubeClient.RbacV1().ClusterRoles().Delete(context.TODO(), metricsTestCRBName, metav1.DeleteOptions{}); err != nil {
		if !kerr.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (f *Framework) revertManifestFileNamespace() error {
	return replaceNamespaceFromManifest(f.OperatorNamespace, "openshift-file-integrity", f.NamespacedManPath)
}

func replaceNamespaceFromManifest(nsFrom, nsTo string, namespacedManPath *string) error {
	if namespacedManPath == nil {
		errors.New("Error: no namespaced manifest given as test argument. operator-sdk might have changed.")
	}
	manPath := *namespacedManPath
	// #nosec
	read, err := ioutil.ReadFile(manPath)
	if err != nil {
		fmt.Errorf("Error reading namespaced manifest file: %s", err)
	}

	newContents := strings.Replace(string(read), nsFrom, nsTo, -1)

	// #nosec
	err = ioutil.WriteFile(manPath, []byte(newContents), 644)
	if err != nil {
		fmt.Errorf("Error writing namespaced manifest file: %s", err)
	}
	return nil
}

func (f *Framework) deleteStatusEvents() error {
	selectors := []string{"reason=FileIntegrityStatus", "reason=NodeIntegrityStatus"}
	for _, sel := range selectors {
		eventList, err := f.KubeClient.CoreV1().Events(f.OperatorNamespace).List(context.TODO(), metav1.ListOptions{FieldSelector: sel})
		if err != nil {
			return err
		}
		for _, ev := range eventList.Items {
			if err := f.KubeClient.CoreV1().Events(f.OperatorNamespace).Delete(context.TODO(), ev.Name, metav1.DeleteOptions{}); err != nil {
				if !kerr.IsNotFound(err) {
					return err
				}
			}
		}
	}
	return nil
}
