package e2e

import (
	goctx "context"
	"testing"

	framework "github.com/operator-framework/operator-sdk/pkg/test"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fileintv1alpha1 "github.com/mrogers950/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/mrogers950/file-integrity-operator/pkg/common"
)

func TestFileIntegritySingleRun(t *testing.T) {
	testctx := setupTestRequirements(t)
	defer testctx.Cleanup()

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
			Name:      "test-check",
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
	t.Log("ds OK")

	// TODO: Check the status of the fileIntegrity object to see that we're doing alright!
}
