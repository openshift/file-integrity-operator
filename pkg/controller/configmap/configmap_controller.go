package configmap

import (
	"context"
	"fmt"
	"time"

	"github.com/mrogers950/file-integrity-operator/pkg/common"
	"k8s.io/apimachinery/pkg/types"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("controller_configmap")

// Add creates a new ConfigMap Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileConfigMap{client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("configmap-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource ConfigMap
	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// TODO(user): Modify this to be the types you create that are owned by the primary resource
	// Watch for changes to secondary resource Pods and requeue the owner ConfigMap
	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &corev1.ConfigMap{},
	})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileConfigMap implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileConfigMap{}

// ReconcileConfigMap reconciles a ConfigMap object
type ReconcileConfigMap struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a ConfigMap object and makes changes based on the state read
// and what is in the ConfigMap.Spec
// TODO(user): Modify this Reconcile function to implement your Controller logic.  This example creates
// a Pod as an example
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileConfigMap) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	if request.Namespace != "openshift-file-integrity" || request.Name != "aide-conf" {
		return reconcile.Result{}, nil
	}

	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling ConfigMap")

	// Fetch the ConfigMap instance
	instance := &corev1.ConfigMap{}
	err := r.client.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	// only continue if the configmap received an update through the user-provided config
	if _, ok := instance.Annotations["fileintegrity.openshift.io/updated"]; !ok {
		reqLogger.Info("DBG: updated annotation not found - removing from queue")
		return reconcile.Result{}, nil
	}

	// handling the re-init daemonSets: these are created by the FileIntegrity controller when the AIDE config has been
	// updated by the user. They touch a file on the node host and then sleep. The file signals to the AIDE pod
	// daemonSets that they need to back up and re-initialize the AIDE database. So once we've confirmed that the
	// re-init daemonSets have started running we can delete them and continue with the rollout of the AIDE pods.
	masterReinitDS := &appsv1.DaemonSet{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: common.MasterReinitDaemonSetName, Namespace: common.FileIntegrityNamespace}, masterReinitDS)
	if err != nil {
		// includes notFound, we will requeue here at least once.
		reqLogger.Error(err, "error getting master reinit daemonSet")
		return reconcile.Result{}, err
	}
	// not ready, requeue
	if !daemonSetIsReady(masterReinitDS) {
		reqLogger.Info("DBG: requeue of master DS")
		return reconcile.Result{RequeueAfter: time.Duration(5 * time.Second)}, nil // guessing on 5 seconds as acceptable requeue rate
	}

	workerReinitDS := &appsv1.DaemonSet{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: common.WorkerReinitDaemonSetName, Namespace: common.FileIntegrityNamespace}, workerReinitDS)
	if err != nil {
		// includes notFound, we will requeue here at least once.
		reqLogger.Error(err, "error getting worker reinit daemonSet")
		return reconcile.Result{}, err
	}
	// not ready, requeue
	if !daemonSetIsReady(workerReinitDS) {
		reqLogger.Info("DBG: requeue of worker DS")
		return reconcile.Result{RequeueAfter: time.Duration(5 * time.Second)}, nil // guessing on 5 seconds as acceptable requeue rate
	}

	reqLogger.Info("reinitDaemonSet statuses", "workerStatus", workerReinitDS.Status, "masterStatus", masterReinitDS.Status)

	// both reinit daemonSets are ready, so we're finished with them
	if err := r.client.Delete(context.TODO(), masterReinitDS); err != nil {
		return reconcile.Result{}, err
	}
	if err := r.client.Delete(context.TODO(), workerReinitDS); err != nil {
		return reconcile.Result{}, err
	}

	ds := &appsv1.DaemonSet{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: common.WorkerDaemonSetName, Namespace: common.FileIntegrityNamespace}, ds)
	if err != nil {
		reqLogger.Error(err, "error getting worker daemonSet")
		return reconcile.Result{}, err
	}

	if err := triggerDaemonSetRollout(r.client, ds); err != nil {
		reqLogger.Error(err, "error triggering worker daemonSet rollout")
		return reconcile.Result{}, err
	}

	ds = &appsv1.DaemonSet{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: common.MasterDaemonSetName, Namespace: common.FileIntegrityNamespace}, ds)
	if err != nil {
		reqLogger.Error(err, "error getting master daemonSet")
		return reconcile.Result{}, err
	}

	if err := triggerDaemonSetRollout(r.client, ds); err != nil {
		reqLogger.Error(err, "error triggering master daemonSet rollout")
		return reconcile.Result{}, err
	}

	reqLogger.Info("DBG: rollout triggered, clearing update annotation")
	// unset update annotation
	conf := instance.DeepCopy()
	conf.Annotations = nil
	if err := r.client.Update(context.TODO(), conf); err != nil {
		reqLogger.Error(err, "error clearing configMap annotations")
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

// triggerDaemonSetRollout restarts the daemonSet pods by adding an annotation to the spec template.
func triggerDaemonSetRollout(c client.Client, ds *appsv1.DaemonSet) error {
	annotations := map[string]string{}
	dscpy := ds.DeepCopy()

	if dscpy.Spec.Template.Annotations == nil {
		dscpy.Spec.Template.Annotations = annotations
	}
	dscpy.Spec.Template.Annotations["fileintegrity.openshift.io/restart-"+fmt.Sprintf("%d", time.Now().Unix())] = ""
	return c.Update(context.TODO(), dscpy)
}

// this method to check ready is used in some of the Origin e2e testing - is it accurate?
func daemonSetIsReady(ds *appsv1.DaemonSet) bool {
	return ds.Status.DesiredNumberScheduled == ds.Status.NumberAvailable
}
