package status

import (
	"context"
	"time"

	controllerruntime "sigs.k8s.io/controller-runtime"

	"github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/controller/metrics"

	"github.com/go-logr/logr"

	"github.com/openshift/file-integrity-operator/pkg/common"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var log = logf.Log.WithName("controller_status")
var statusRequeue = time.Second * 30

// Add creates a new FileIntegrity Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddStatusController(mgr manager.Manager, met *metrics.Metrics) error {
	r := &StatusReconciler{client: mgr.GetClient(), scheme: mgr.GetScheme(),
		recorder: mgr.GetEventRecorderFor("statusctrl"), metrics: met,
	}
	return controllerruntime.NewControllerManagedBy(mgr).
		Named("status-controller").
		For(&v1alpha1.FileIntegrity{}).
		Owns(&v1alpha1.FileIntegrityNodeStatus{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&appsv1.DaemonSet{}).
		Complete(r)
}

// blank assignment to verify that StatusReconciler implements reconcile.Reconciler
var _ reconcile.Reconciler = &StatusReconciler{}

// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
// Reconcile handles the creation and update of configMaps as well as the initial daemonSets for the AIDE pods.
func (r *StatusReconciler) StatusReconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("reconciling FileIntegrityStatus")

	// Fetch the FileIntegrity instance
	instance := &v1alpha1.FileIntegrity{}
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

	reinitDsList := &appsv1.DaemonSetList{}
	if err := r.client.List(context.TODO(), reinitDsList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{common.IntegrityReinitOwnerLabelKey: instance.Name}),
	}); err != nil {
		return reconcile.Result{}, err
	}

	// Found, so we are initializing
	if len(reinitDsList.Items) > 0 {
		statusErr := r.updateStatus(reqLogger, instance, v1alpha1.PhaseInitializing)
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
		updateErr := r.updateStatus(reqLogger, instance, v1alpha1.PhaseInitializing)
		if updateErr != nil {
			reqLogger.Error(updateErr, "error updating FileIntegrity status")
			return reconcile.Result{}, updateErr
		}
		return reconcile.Result{RequeueAfter: statusRequeue}, nil
	}

	// both daemonSets were missing, so we're currently inactive.
	err = r.updateStatus(reqLogger, instance, v1alpha1.PhasePending)
	if err != nil {
		reqLogger.Error(err, "error updating FileIntegrity status")
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: statusRequeue}, nil
}

// mapActiveStatus returns the FileIntegrityStatus relative to the node status; If any nodes have an error, return
// PhaseError, otherwise return PhaseActive.
func (r *StatusReconciler) mapActiveStatus(integrity *v1alpha1.FileIntegrity) (v1alpha1.FileIntegrityStatusPhase, error) {
	listOpts := client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{common.IntegrityOwnerLabelKey: integrity.Name}),
	}

	nodeStatusList := v1alpha1.FileIntegrityNodeStatusList{}
	if err := r.client.List(context.TODO(), &nodeStatusList, &listOpts); err != nil {
		return v1alpha1.PhaseError, err
	}

	nodeList := corev1.NodeList{}
	if err := r.client.List(context.TODO(), &nodeList, &client.ListOptions{}); err != nil {
		return v1alpha1.PhaseError, err
	}
	nodeNameList := make(map[string]bool)
	for _, node := range nodeList.Items {
		nodeNameList[node.Name] = true
	}

	for _, nodeStatus := range nodeStatusList.Items {
		// Check if the node is still there, and remove the node status if it's not.
		// This is to handle the case where the node is deleted, but the node status is not.
		if _, ok := nodeNameList[nodeStatus.NodeName]; !ok {
			// If the node is not there we will just delete it.
			if err := r.client.Delete(context.TODO(), &nodeStatus); err != nil {
				return v1alpha1.PhaseError, err
			}

		}
		if nodeStatus.LastResult.Condition == v1alpha1.NodeConditionErrored {
			return v1alpha1.PhaseError, nil
		}
	}
	return v1alpha1.PhaseActive, nil
}

func (r *StatusReconciler) updateStatus(logger logr.Logger, integrity *v1alpha1.FileIntegrity, phase v1alpha1.FileIntegrityStatusPhase) error {
	if integrity.Status.Phase != phase {
		integrityCopy := integrity.DeepCopy()
		integrityCopy.Status.Phase = phase

		logger.Info("Updating status", "Name", integrityCopy.Name, "Phase", integrityCopy.Status.Phase)
		err := r.client.Status().Update(context.TODO(), integrityCopy)
		if err != nil {
			return err
		}

		// Set the event type accordingly and increment Metrics.
		eventType := corev1.EventTypeNormal
		if integrityCopy.Status.Phase == v1alpha1.PhaseError {
			r.metrics.IncFileIntegrityPhaseError()
			eventType = corev1.EventTypeWarning
		} else {
			switch integrityCopy.Status.Phase {
			case v1alpha1.PhaseInitializing:
				r.metrics.IncFileIntegrityPhaseInit()
			case v1alpha1.PhaseActive:
				r.metrics.IncFileIntegrityPhaseActive()
			case v1alpha1.PhasePending:
				r.metrics.IncFileIntegrityPhasePending()
			}
		}

		// Create an event for the transition. 'tegrity.
		r.recorder.Eventf(integrity, eventType, "FileIntegrityStatus", "%s", integrityCopy.Status.Phase)
	}
	return nil
}
