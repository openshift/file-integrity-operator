package configmap

import (
	"context"
	"github.com/openshift/file-integrity-operator/pkg/controller/metrics"
	"strconv"
	"time"

	"github.com/go-logr/logr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	fileintegrityv1alpha1 "github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
)

var log = logf.Log.WithName("controller_configmap")

// Add creates a new ConfigMap Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, met *metrics.Metrics) error {
	return add(mgr, newReconciler(mgr, met))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, met *metrics.Metrics) reconcile.Reconciler {
	return &ReconcileConfigMap{client: mgr.GetClient(), scheme: mgr.GetScheme(),
		recorder: mgr.GetEventRecorderFor("configmapctrl"),
		metrics:  met,
	}
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
	client   client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder
	metrics  *metrics.Metrics
}

// Reconcile reads that state of the cluster for a ConfigMap object and makes changes based on the state read
// and what is in the ConfigMap.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileConfigMap) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling ConfigMap")

	// Fetch the ConfigMap instance
	instance := &corev1.ConfigMap{}
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

	if common.IsAideConfig(instance.Labels) {
		return r.reconcileAideConf(instance, reqLogger)
	} else if common.IsIntegrityLog(instance.Labels) {
		return r.handleIntegrityLog(instance, reqLogger)
	}

	return reconcile.Result{}, nil
}

func (r *ReconcileConfigMap) reconcileAideConf(instance *corev1.ConfigMap, logger logr.Logger) (reconcile.Result, error) {
	// only continue if the configmap received an update through the user-provided config
	if _, ok := instance.Annotations[common.AideConfigUpdatedAnnotationKey]; !ok {
		return reconcile.Result{}, nil
	}

	// handling the re-init daemonSets: these are created by the FileIntegrity controller when the AIDE config has been
	// updated by the user. They touch a file on the node host and then sleep. The file signals to the AIDE pod
	// daemonSets that they need to back up and re-initialize the AIDE database.
	reinitDS := &appsv1.DaemonSet{}
	reinitDSName := common.GetReinitDaemonSetName(instance.Name)
	err := r.client.Get(context.TODO(), types.NamespacedName{Name: reinitDSName, Namespace: common.FileIntegrityNamespace}, reinitDS)
	if err != nil && kerr.IsNotFound(err) {
		logger.Info("not found, requeuing", "dsName", reinitDSName)
		return reconcile.Result{Requeue: true}, nil
	} else if err != nil {
		logger.Error(err, "error getting reinit daemonSet")
		return reconcile.Result{}, err
	}

	// not ready, requeue
	if !common.DaemonSetIsReady(reinitDS) {
		return reconcile.Result{RequeueAfter: time.Duration(time.Second)}, nil
	}

	logger.Info("re-init daemonSet finished, removing")

	// reinit daemonSet is ready, so we're finished with it
	if err := r.client.Delete(context.TODO(), reinitDS); err != nil {
		return reconcile.Result{}, err
	}

	r.metrics.IncFileIntegrityReinitDaemonsetDelete()
	// unset update annotation
	conf := instance.DeepCopy()
	conf.Annotations = nil
	if err := r.client.Update(context.TODO(), conf); err != nil {
		logger.Error(err, "error clearing configMap annotations")
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *ReconcileConfigMap) handleIntegrityLog(cm *corev1.ConfigMap, logger logr.Logger) (reconcile.Result, error) {
	owner, err := common.GetConfigMapOwnerName(cm)
	if err != nil {
		logger.Error(err, "Malformed ConfigMap: Could not get owner. Cannot retry.")
		return reconcile.Result{}, nil
	}

	node, err := common.GetConfigMapNodeName(cm)
	if err != nil {
		logger.Error(err, "Malformed ConfigMap: Could not get node. Cannot retry.")
		return reconcile.Result{}, nil
	}

	cachedfi := &fileintegrityv1alpha1.FileIntegrity{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: owner, Namespace: cm.Namespace}, cachedfi)
	if err != nil {
		return reconcile.Result{}, err
	}
	fi := cachedfi.DeepCopy()

	if common.IsIntegrityLogAnError(cm) {
		errorMsg, containsErrorAnnotation := cm.Annotations[common.IntegrityLogErrorAnnotationKey]
		if !containsErrorAnnotation {
			_, containsContentKey := cm.Data[common.IntegrityLogContentKey]
			if !containsContentKey {
				errorMsg = "log ConfigMap doesn't contain content"
			} else {
				errorMsg = "unknown error"
			}
		}

		status := fileintegrityv1alpha1.FileIntegrityScanResult{
			Condition:     fileintegrityv1alpha1.NodeConditionErrored,
			LastProbeTime: cm.GetCreationTimestamp(),
			ErrorMsg:      errorMsg,
		}

		if err := r.createOrUpdateNodeStatus(node, fi, status); err != nil {
			return reconcile.Result{}, err
		}
	} else if common.IsIntegrityLogAFailure(cm) {
		failedCM := getConfigMapForFailureLog(cm)
		if err = r.client.Create(context.TODO(), failedCM); err != nil {
			// Update if it already existed
			if kerr.IsAlreadyExists(err) {
				if err = r.client.Update(context.TODO(), failedCM); err != nil {
					return reconcile.Result{}, err
				}
			} else {
				return reconcile.Result{}, err
			}
		}

		status := fileintegrityv1alpha1.FileIntegrityScanResult{
			Condition:                fileintegrityv1alpha1.NodeConditionFailed,
			LastProbeTime:            cm.GetCreationTimestamp(),
			ResultConfigMapName:      failedCM.Name,
			ResultConfigMapNamespace: failedCM.Namespace,
		}

		status.FilesAdded, _ = strconv.Atoi(failedCM.Annotations[common.IntegrityLogFilesAddedAnnotation])
		status.FilesRemoved, _ = strconv.Atoi(failedCM.Annotations[common.IntegrityLogFilesRemovedAnnotation])
		status.FilesChanged, _ = strconv.Atoi(failedCM.Annotations[common.IntegrityLogFilesChangedAnnotation])

		if err := r.createOrUpdateNodeStatus(node, fi, status); err != nil {
			return reconcile.Result{}, err
		}
	} else {
		status := fileintegrityv1alpha1.FileIntegrityScanResult{
			Condition:     fileintegrityv1alpha1.NodeConditionSucceeded,
			LastProbeTime: cm.GetCreationTimestamp(),
		}
		if err := r.createOrUpdateNodeStatus(node, fi, status); err != nil {
			return reconcile.Result{}, err
		}
	}

	// No need to keep the ConfigMap, the log collector will try to create
	// another one on its next run
	if err = r.client.Delete(context.TODO(), cm); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

// Creates or updates a FileIntegrityNodeStatus object for the node. If a result exists for a node matching the new result, we update that result.
// At the most there will be three results per status. One for each condition type. The most recently updated reflects the current result.
func (r *ReconcileConfigMap) createOrUpdateNodeStatus(node string, instance *fileintegrityv1alpha1.FileIntegrity, new fileintegrityv1alpha1.FileIntegrityScanResult) error {
	nodeStatus := &fileintegrityv1alpha1.FileIntegrityNodeStatus{}
	err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: instance.Namespace, Name: instance.Name + "-" + node}, nodeStatus)
	if err != nil && !kerr.IsNotFound(err) {
		return err
	}

	if kerr.IsNotFound(err) {
		// This node does not have a corresponding FileIntegrityNodeStatus yet, create with this initial result.
		nodeStatus = &fileintegrityv1alpha1.FileIntegrityNodeStatus{
			ObjectMeta: metav1.ObjectMeta{
				Name:      instance.Name + "-" + node,
				Namespace: instance.Namespace,
				Labels: map[string]string{
					common.IntegrityOwnerLabelKey: instance.Name,
				},
			},
			NodeName: node,
			Results:  []fileintegrityv1alpha1.FileIntegrityScanResult{},
		}
		nodeStatus.Results = append(nodeStatus.Results, new)
		nodeStatus.LastResult = *new.DeepCopy()
		refErr := controllerutil.SetControllerReference(instance, nodeStatus, r.scheme)
		if refErr != nil {
			return refErr
		}

		createErr := r.client.Create(context.TODO(), nodeStatus)
		if createErr != nil {
			return createErr
		}

		updateNodeStatusMetrics(r, nodeStatus)
		createNodeStatusEvent(r, instance, nodeStatus)
		return nil
	}

	updateResults := make([]fileintegrityv1alpha1.FileIntegrityScanResult, 0)
	// Filter to keep the other results. We only want to replace one of the same.
	for _, result := range nodeStatus.Results {
		if result.Condition != new.Condition {
			updateResults = append(updateResults, result)
			if isLatestScanResult(result, nodeStatus) {
				nodeStatus.LastResult = *result.DeepCopy()
			}
		}
	}

	statusCopy := nodeStatus.DeepCopy()

	updateResults = append(updateResults, new)
	statusCopy.Results = updateResults
	if isLatestScanResult(new, nodeStatus) {
		statusCopy.LastResult = *new.DeepCopy()
	}

	updateErr := r.client.Update(context.TODO(), statusCopy)
	if updateErr != nil {
		return updateErr
	}

	// Create an event if there was a transition or an updated Failure
	if conditionIsNewFailureOrTransition(nodeStatus, statusCopy) {
		updateNodeStatusMetrics(r, statusCopy)
		createNodeStatusEvent(r, instance, statusCopy)
	}
	return nil
}

// isLatestScanResult returns true if result is newer than nodeStatus.LastResult
func isLatestScanResult(result fileintegrityv1alpha1.FileIntegrityScanResult, nodeStatus *fileintegrityv1alpha1.FileIntegrityNodeStatus) bool {
	return result.LastProbeTime.After(nodeStatus.LastResult.LastProbeTime.Time)
}

// conditionIsNewFailureOrTransition return true if cur has an updated failure count over prev (if both were failed conditions),
// or if cur's condition is different than prev.
func conditionIsNewFailureOrTransition(prev, cur *fileintegrityv1alpha1.FileIntegrityNodeStatus) bool {
	if cur.LastResult.Condition == fileintegrityv1alpha1.NodeConditionFailed && prev.LastResult.Condition == fileintegrityv1alpha1.NodeConditionFailed {
		return cur.LastResult.FilesRemoved != prev.LastResult.FilesRemoved ||
			cur.LastResult.FilesAdded != prev.LastResult.FilesAdded ||
			cur.LastResult.FilesChanged != prev.LastResult.FilesChanged
	} else if cur.LastResult.Condition != prev.LastResult.Condition {
		return true
	}
	return false
}

// createNodeStatusEvent creates an event to report the latest check result
func createNodeStatusEvent(r *ReconcileConfigMap, fi *fileintegrityv1alpha1.FileIntegrity, status *fileintegrityv1alpha1.FileIntegrityNodeStatus) {
	switch status.LastResult.Condition {
	case fileintegrityv1alpha1.NodeConditionSucceeded:
		r.recorder.Eventf(fi, corev1.EventTypeNormal, "NodeIntegrityStatus", "no changes to node %s",
			status.NodeName)
	case fileintegrityv1alpha1.NodeConditionFailed:
		r.recorder.Eventf(fi, corev1.EventTypeWarning, "NodeIntegrityStatus",
			"node %s has changed! a:%d,c:%d,r:%d log:%s/%s", status.NodeName,
			status.LastResult.FilesAdded, status.LastResult.FilesChanged, status.LastResult.FilesRemoved,
			status.LastResult.ResultConfigMapNamespace, status.LastResult.ResultConfigMapName)
	case fileintegrityv1alpha1.NodeConditionErrored:
		r.recorder.Eventf(fi, corev1.EventTypeWarning, "NodeIntegrityStatus",
			"node %s has an error! %s", status.NodeName, status.LastResult.ErrorMsg)
	}
}

func getConfigMapForFailureLog(cm *corev1.ConfigMap) *corev1.ConfigMap {
	failedCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        cm.Name + "-failed",
			Namespace:   cm.Namespace,
			Labels:      cm.Labels,
			Annotations: cm.Annotations,
		},
		Data: cm.Data,
	}
	// We remove the log label so we don't queue the new ConfigMap
	delete(failedCM.Labels, common.IntegrityLogLabelKey)
	// We mark is as a result
	failedCM.Labels[common.IntegrityLogResultLabelKey] = ""
	return failedCM
}

func updateNodeStatusMetrics(r *ReconcileConfigMap, status *fileintegrityv1alpha1.FileIntegrityNodeStatus) {
	r.metrics.IncFileIntegrityNodeStatus(string(status.LastResult.Condition), status.NodeName)

	switch status.LastResult.Condition {
	case fileintegrityv1alpha1.NodeConditionSucceeded:
		r.metrics.SetFileIntegrityNodeStatusGaugeGood(status.NodeName)
	case fileintegrityv1alpha1.NodeConditionFailed:
		r.metrics.SetFileIntegrityNodeStatusGaugeBad(status.NodeName)
	case fileintegrityv1alpha1.NodeConditionErrored:
		r.metrics.IncFileIntegrityNodeStatusError(status.LastResult.ErrorMsg, status.NodeName)
	}
}
