package status

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	fileintegrityv1alpha1 "github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
)

var log = logf.Log.WithName("controller_status")
var statusRequeue = time.Second * 30

// Add creates a new FileIntegrity Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileFileIntegrityStatus{client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("status-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource FileIntegrity
	err = c.Watch(&source.Kind{Type: &fileintegrityv1alpha1.FileIntegrity{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}
	// XXX also watch for configmaps, init daemonset

	return nil
}

// blank assignment to verify that ReconcileFileIntegrity implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileFileIntegrityStatus{}

// ReconcileFileIntegrity reconciles a FileIntegrity object
type ReconcileFileIntegrityStatus struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
}

// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
// Reconcile handles the creation and update of configMaps as well as the initial daemonSets for the AIDE pods.
func (r *ReconcileFileIntegrityStatus) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("reconciling FileIntegrityStatus")

	// Fetch the FileIntegrity instance
	instance := &fileintegrityv1alpha1.FileIntegrity{}
	err := r.client.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if kerr.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	// first check is for the reinit daemonset, when this exists at all we are in an initialization phase.
	reinitDS := &appsv1.DaemonSet{}
	reinitDSName := common.GetReinitDaemonSetName(instance.Name)
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: reinitDSName, Namespace: common.FileIntegrityNamespace}, reinitDS)
	if err != nil && !kerr.IsNotFound(err) {
		reqLogger.Error(err, "error getting reinit daemonSet")
		return reconcile.Result{}, err
	}
	if err == nil {
		// reinit daemonset is active, thus we are initializing
		err := updateStatus(r.client, instance, fileintegrityv1alpha1.PhaseInitializing)
		if err != nil {
			reqLogger.Error(err, "error updating FileIntegrity status")
			return reconcile.Result{}, err
		}
		return reconcile.Result{RequeueAfter: statusRequeue}, nil
	}

	ds := &appsv1.DaemonSet{}
	dsName := common.GetDaemonSetName(instance.Name)
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: dsName, Namespace: common.FileIntegrityNamespace}, ds)
	if err != nil && !kerr.IsNotFound(err) {
		reqLogger.Error(err, "error getting daemonSet")
		return reconcile.Result{}, err
	}

	if err == nil {
		if common.DaemonSetIsReady(ds) && !common.DaemonSetIsUpdating(ds) {
			err := updateStatus(r.client, instance, fileintegrityv1alpha1.PhaseActive)
			if err != nil {
				reqLogger.Error(err, "error updating FileIntegrity status")
				return reconcile.Result{}, err
			}
			return reconcile.Result{RequeueAfter: statusRequeue}, nil
		}
		// Not ready, set to initializing
		err := updateStatus(r.client, instance, fileintegrityv1alpha1.PhaseInitializing)
		if err != nil {
			reqLogger.Error(err, "error updating FileIntegrity status")
			return reconcile.Result{}, err
		}
		return reconcile.Result{RequeueAfter: statusRequeue}, nil
	}

	// both daemonSets were missing, so we're currently inactive.
	err = updateStatus(r.client, instance, fileintegrityv1alpha1.PhasePending)
	if err != nil {
		reqLogger.Error(err, "error updating FileIntegrity status")
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: statusRequeue}, nil
}

func updateStatus(client client.Client, integrity *fileintegrityv1alpha1.FileIntegrity, phase fileintegrityv1alpha1.FileIntegrityStatusPhase) error {
	if integrity.Status.Phase != phase {
		integrityCpy := integrity.DeepCopy()
		integrityCpy.Status.Phase = phase

		return client.Status().Update(context.TODO(), integrityCpy)
	}
	return nil
}
