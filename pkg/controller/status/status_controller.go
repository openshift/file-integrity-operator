package status

import (
	"context"
	"time"

	"github.com/openshift/file-integrity-operator/pkg/controller/metrics"

	"github.com/go-logr/logr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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
func Add(mgr manager.Manager, met *metrics.Metrics) error {
	return add(mgr, newReconciler(mgr, met))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, met *metrics.Metrics) reconcile.Reconciler {
	return &ReconcileFileIntegrityStatus{client: mgr.GetClient(), scheme: mgr.GetScheme(),
		recorder: mgr.GetEventRecorderFor("statusctrl"), metrics: met,
	}
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

	// Reconcile on FileIntegrityNodeStatus updates
	err = c.Watch(&source.Kind{Type: &fileintegrityv1alpha1.FileIntegrityNodeStatus{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &fileintegrityv1alpha1.FileIntegrity{},
	})
	if err != nil {
		return err
	}

	// Reconcile on configMap updates
	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &fileintegrityv1alpha1.FileIntegrity{},
	})
	if err != nil {
		return err
	}

	// Reconcile on daemonSet updates
	err = c.Watch(&source.Kind{Type: &appsv1.DaemonSet{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &fileintegrityv1alpha1.FileIntegrity{},
	})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileFileIntegrity implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileFileIntegrityStatus{}

// ReconcileFileIntegrity reconciles a FileIntegrity object
type ReconcileFileIntegrityStatus struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client   client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder
	metrics  *metrics.Metrics
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

	// List the nodes associated with the FI
	nodeList, getErr := r.getFileIntegrityNodes(instance)
	if getErr != nil {
		return reconcile.Result{}, getErr
	}

	// If any of the associated nodes have an active re-init daemonSet, then the FI is initializing
	for _, node := range nodeList.Items {
		nodeName := node.Name
		ds := &appsv1.DaemonSet{}
		dsName := common.ReinitDaemonSetNodeName(instance.Name, nodeName)
		err = r.client.Get(context.TODO(), types.NamespacedName{Name: dsName, Namespace: common.FileIntegrityNamespace}, ds)
		if err != nil && !kerr.IsNotFound(err) {
			reqLogger.Error(err, "error getting reinit daemonSet")
			return reconcile.Result{}, err
		}
		if err == nil {
			// One of the reinit daemonsets for the node group is present
			reqLogger.Info("node has an active re-init", "Name", nodeName, "daemonSet name", ds.Name)
			updateErr := r.updateStatus(reqLogger, instance, fileintegrityv1alpha1.PhaseInitializing)
			if updateErr != nil {
				reqLogger.Error(updateErr, "error updating FileIntegrity status")
				return reconcile.Result{}, updateErr
			}
			return reconcile.Result{RequeueAfter: statusRequeue}, nil
		}
	}

	// There was no node-specific daemonset, so now try to handle the general one to see if the FI is initializing
	generalDs := &appsv1.DaemonSet{}
	generalDsName := common.ReinitDaemonSetName(instance.Name)
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: generalDsName, Namespace: common.FileIntegrityNamespace}, generalDs)
	if err != nil && !kerr.IsNotFound(err) {
		reqLogger.Error(err, "error getting reinit daemonSet")
		return reconcile.Result{}, err
	}

	// Found, so we are initializing
	if err == nil {
		statusErr := r.updateStatus(reqLogger, instance, fileintegrityv1alpha1.PhaseInitializing)
		if statusErr != nil {
			reqLogger.Error(statusErr, "error updating FileIntegrity status")
			return reconcile.Result{}, statusErr
		}
		return reconcile.Result{RequeueAfter: statusRequeue}, nil
	}

	ds := &appsv1.DaemonSet{}
	dsName := common.DaemonSetName(instance.Name)
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: dsName, Namespace: common.FileIntegrityNamespace}, ds)
	if err != nil && !kerr.IsNotFound(err) {
		reqLogger.Error(err, "error getting daemonSet")
		return reconcile.Result{}, err
	}

	if err == nil {
		if common.DaemonSetIsReady(ds) && !common.DaemonSetIsUpdating(ds) {
			phase, mapStatusErr := r.mapActiveStatus(instance)
			if mapStatusErr != nil {
				reqLogger.Error(mapStatusErr, "error getting FileIntegrityNodeStatusList")
				return reconcile.Result{}, mapStatusErr
			}

			updateErr := r.updateStatus(reqLogger, instance, phase)
			if updateErr != nil {
				reqLogger.Error(updateErr, "error updating FileIntegrity status")
				return reconcile.Result{}, updateErr
			}
			return reconcile.Result{RequeueAfter: statusRequeue}, nil
		}
		// Not ready, set to initializing
		updateErr := r.updateStatus(reqLogger, instance, fileintegrityv1alpha1.PhaseInitializing)
		if updateErr != nil {
			reqLogger.Error(updateErr, "error updating FileIntegrity status")
			return reconcile.Result{}, updateErr
		}
		return reconcile.Result{RequeueAfter: statusRequeue}, nil
	}

	// both daemonSets were missing, so we're currently inactive.
	err = r.updateStatus(reqLogger, instance, fileintegrityv1alpha1.PhasePending)
	if err != nil {
		reqLogger.Error(err, "error updating FileIntegrity status")
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: statusRequeue}, nil
}

// mapActiveStatus returns the FileIntegrityStatus relative to the node status; If any nodes have an error, return
// PhaseError, otherwise return PhaseActive.
func (r *ReconcileFileIntegrityStatus) mapActiveStatus(integrity *fileintegrityv1alpha1.FileIntegrity) (fileintegrityv1alpha1.FileIntegrityStatusPhase, error) {
	listOpts := client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{common.IntegrityOwnerLabelKey: integrity.Name}),
	}

	nodeStatusList := fileintegrityv1alpha1.FileIntegrityNodeStatusList{}
	if err := r.client.List(context.TODO(), &nodeStatusList, &listOpts); err != nil {
		return fileintegrityv1alpha1.PhaseError, err
	}

	for _, nodeStatus := range nodeStatusList.Items {
		if nodeStatus.LastResult.Condition == fileintegrityv1alpha1.NodeConditionErrored {
			return fileintegrityv1alpha1.PhaseError, nil
		}
	}

	return fileintegrityv1alpha1.PhaseActive, nil
}

func (r *ReconcileFileIntegrityStatus) updateStatus(logger logr.Logger, integrity *fileintegrityv1alpha1.FileIntegrity, phase fileintegrityv1alpha1.FileIntegrityStatusPhase) error {
	if integrity.Status.Phase != phase {
		integrityCopy := integrity.DeepCopy()
		integrityCopy.Status.Phase = phase

		logger.Info("Updating status", "Name", integrityCopy.Name, "Phase", integrityCopy.Status.Phase)
		err := r.client.Status().Update(context.TODO(), integrityCopy)
		if err != nil {
			return err
		}

		// Set the event type accordingly and increment metrics.
		eventType := corev1.EventTypeNormal
		if integrityCopy.Status.Phase == fileintegrityv1alpha1.PhaseError {
			r.metrics.IncFileIntegrityPhaseError()
			eventType = corev1.EventTypeWarning
		} else {
			switch integrityCopy.Status.Phase {
			case fileintegrityv1alpha1.PhaseInitializing:
				r.metrics.IncFileIntegrityPhaseInit()
			case fileintegrityv1alpha1.PhaseActive:
				r.metrics.IncFileIntegrityPhaseActive()
			case fileintegrityv1alpha1.PhasePending:
				r.metrics.IncFileIntegrityPhasePending()
			}
		}

		// Create an event for the transition. 'tegrity.
		r.recorder.Eventf(integrity, eventType, "FileIntegrityStatus", "%s", integrityCopy.Status.Phase)
	}
	return nil
}

func (r *ReconcileFileIntegrityStatus) getFileIntegrityNodes(instance *fileintegrityv1alpha1.FileIntegrity) (*corev1.NodeList, error) {
	listOpts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(instance.Spec.NodeSelector),
	}
	nodes := &corev1.NodeList{}
	err := r.client.List(context.TODO(), nodes, listOpts)
	if err != nil {
		return nil, err
	}
	return nodes, nil
}
