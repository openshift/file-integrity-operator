package framework

import (
	"os"
	"testing"
	"time"

	"github.com/pborman/uuid"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
)

type Context struct {
	id         string
	cleanupFns []cleanupFn
	// the  namespace is deprecated
	// todo: remove before 1.0.0
	// use operatorNamespace or watchNamespace  instead
	namespace         string
	operatorNamespace string
	watchNamespace    string
	t                 *testing.T

	namespacedManPath  string
	client             *frameworkClient
	kubeclient         kubernetes.Interface
	restMapper         *restmapper.DeferredDiscoveryRESTMapper
	skipCleanupOnError bool
}

// todo(camilamacedo86): Remove the following line just added for we are able to deprecated TestCtx
// need to be done before: 1.0.0

// Deprecated: TestCtx exists for historical compatibility. Use Context instead.
type TestCtx = Context //nolint:golint

type CleanupOptions struct {
	TestContext   *Context
	Timeout       time.Duration
	RetryInterval time.Duration
}

type cleanupFn func() error

func (f *Framework) newContext(t *testing.T) *Context {

	// Context is used among others for namespace names where '/' is forbidden and must be 63 characters or less
	id := "osdk-e2e-" + uuid.New()

	var operatorNamespace string
	_, ok := os.LookupEnv(TestOperatorNamespaceEnv)
	if ok {
		operatorNamespace = f.OperatorNamespace
	}

	watchNamespace := operatorNamespace
	ns, ok := os.LookupEnv(TestWatchNamespaceEnv)
	if ok {
		watchNamespace = ns
	}

	return &Context{
		id:                 id,
		t:                  t,
		namespace:          operatorNamespace,
		operatorNamespace:  operatorNamespace,
		watchNamespace:     watchNamespace,
		namespacedManPath:  *f.NamespacedManPath,
		client:             f.Client,
		kubeclient:         f.KubeClient,
		restMapper:         f.restMapper,
		skipCleanupOnError: f.SkipCleanupOnError,
	}
}

// This context is intended to be used across tests in a suite, so it doesn't
// contain the testing instance that's included when using newContext.
// Eventually, we will consolidate this method with newContext and only use
// contexts for test suites instead of for individual tests.
func (f *Framework) newSuiteContext() *Context {

	// Context is used among others for namespace names where '/' is forbidden and must be 63 characters or less
	id := "osdk-e2e-" + uuid.New()

	var operatorNamespace string
	_, ok := os.LookupEnv(TestOperatorNamespaceEnv)
	if ok {
		operatorNamespace = f.OperatorNamespace
	}

	watchNamespace := operatorNamespace
	ns, ok := os.LookupEnv(TestWatchNamespaceEnv)
	if ok {
		watchNamespace = ns
	}

	return &Context{
		id:                 id,
		namespace:          operatorNamespace,
		operatorNamespace:  operatorNamespace,
		watchNamespace:     watchNamespace,
		namespacedManPath:  *f.NamespacedManPath,
		client:             f.Client,
		kubeclient:         f.KubeClient,
		restMapper:         f.restMapper,
		skipCleanupOnError: f.SkipCleanupOnError,
	}
}

func NewContext(t *testing.T) *Context {
	return Global.newContext(t)
}

func NewSuiteContext() *Context {
	return Global.newSuiteContext()
}

func (ctx *Context) GetID() string {
	return ctx.id
}

func (ctx *Context) Cleanup() {
	if ctx.t != nil {
		// The cleanup function will be skipped
		if ctx.t.Failed() && ctx.skipCleanupOnError {
			// Also, could we log the error here?
			log.Info("Skipping cleanup function since --skip-cleanup-error is true")
			return
		}
	}
	failed := false
	for i := len(ctx.cleanupFns) - 1; i >= 0; i-- {
		err := ctx.cleanupFns[i]()
		if err != nil {
			failed = true
			if ctx.t != nil {
				ctx.t.Errorf("A cleanup function failed with error: (%v)\n", err)
			} else {
				log.Errorf("A cleanup function failed with error: (%v)", err)
			}
		}
	}
	if ctx.t == nil && failed {
		log.Fatal("A cleanup function failed")
	}
}

func (ctx *Context) AddCleanupFn(fn cleanupFn) {
	ctx.cleanupFns = append(ctx.cleanupFns, fn)
}
