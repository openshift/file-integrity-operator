package configmap

import (
	"context"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
		if errors.IsNotFound(err) {
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
		logger.Info("DBG: updated annotation not found - removing from queue")
		return reconcile.Result{}, nil
	}

	// handling the re-init daemonSets: these are created by the FileIntegrity controller when the AIDE config has been
	// updated by the user. They touch a file on the node host and then sleep. The file signals to the AIDE pod
	// daemonSets that they need to back up and re-initialize the AIDE database. So once we've confirmed that the
	// re-init daemonSets have started running we can delete them and continue with the rollout of the AIDE pods.
	reinitDS := &appsv1.DaemonSet{}
	reinitDSName := common.GetReinitDaemonSetName(instance.Name)
	err := r.client.Get(context.TODO(), types.NamespacedName{Name: reinitDSName, Namespace: common.FileIntegrityNamespace}, reinitDS)
	if err != nil {
		// includes notFound, we will requeue here at least once.
		logger.Error(err, "error getting reinit daemonSet")
		return reconcile.Result{}, err
	}
	// not ready, requeue
	if !common.DaemonSetIsReady(reinitDS) {
		logger.Info("DBG: requeue of DS")
		return reconcile.Result{RequeueAfter: time.Duration(5 * time.Second)}, nil // guessing on 5 seconds as acceptable requeue rate
	}

	logger.Info("reinitDaemonSet statuses", "Status", reinitDS.Status)

	// reinit daemonSet is ready, so we're finished with it
	if err := r.client.Delete(context.TODO(), reinitDS); err != nil {
		return reconcile.Result{}, err
	}

	ds := &appsv1.DaemonSet{}
	dsName := common.GetDaemonSetName(instance.Name)
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: dsName, Namespace: common.FileIntegrityNamespace}, ds)
	if err != nil {
		logger.Error(err, "error getting daemonSet")
		return reconcile.Result{}, err
	}

	if err := deleteDaemonSetPods(r.client, ds); err != nil {
		logger.Error(err, "error deleting daemonSet pods")
		return reconcile.Result{}, err
	}

	logger.Info("DBG: rollout triggered, clearing update annotation")
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

		status := fileintegrityv1alpha1.NodeStatus{
			Condition:     fileintegrityv1alpha1.NodeConditionErrored,
			NodeName:      node,
			LastProbeTime: cm.GetCreationTimestamp(),
			ErrorMsg:      errorMsg,
		}
		if err := r.updateStatus(fi, status); err != nil {
			return reconcile.Result{}, err
		}
	} else if common.IsIntegrityLogAFailure(cm) {
		failedCM := getConfigMapForFailureLog(cm)
		if err = r.client.Create(context.TODO(), failedCM); err != nil {
			// Update if it already existed
			if errors.IsAlreadyExists(err) {
				if err = r.client.Update(context.TODO(), failedCM); err != nil {
					return reconcile.Result{}, err
				}
			} else {
				return reconcile.Result{}, err
			}
		}

		status := fileintegrityv1alpha1.NodeStatus{
			Condition:                fileintegrityv1alpha1.NodeConditionFailed,
			NodeName:                 node,
			LastProbeTime:            cm.GetCreationTimestamp(),
			ResultConfigMapName:      failedCM.Name,
			ResultConfigMapNamespace: failedCM.Namespace,
		}

		status.FilesAdded, _ = strconv.Atoi(failedCM.Annotations[common.IntegrityLogFilesAddedAnnotation])
		status.FilesRemoved, _ = strconv.Atoi(failedCM.Annotations[common.IntegrityLogFilesRemovedAnnotation])
		status.FilesChanged, _ = strconv.Atoi(failedCM.Annotations[common.IntegrityLogFilesChangedAnnotation])

		if err := r.updateStatus(fi, status); err != nil {
			return reconcile.Result{}, err
		}
	} else {
		status := fileintegrityv1alpha1.NodeStatus{
			Condition:     fileintegrityv1alpha1.NodeConditionSucceeded,
			NodeName:      node,
			LastProbeTime: cm.GetCreationTimestamp(),
		}
		if err := r.updateStatus(fi, status); err != nil {
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

func (r *ReconcileConfigMap) updateStatus(fi *fileintegrityv1alpha1.FileIntegrity, status fileintegrityv1alpha1.NodeStatus) error {
	statuses := make([]fileintegrityv1alpha1.NodeStatus, 0)

	// Collect all the status entries except for those containing the current node and == cond argument. We'll be
	// updating that entry below. We want to keep entries for the current node that do not match cond, so that the
	// last status timestamp of a previous condition remains.
	for i, _ := range fi.Status.Statuses {
		if fi.Status.Statuses[i].NodeName == status.NodeName && fi.Status.Statuses[i].Condition == status.Condition {
			continue
		}
		statuses = append(statuses, fi.Status.Statuses[i])
	}
	statuses = append(statuses, status)
	fi.Status.Statuses = statuses

	return r.client.Status().Update(context.TODO(), fi)
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

func deleteDaemonSetPods(c client.Client, ds *appsv1.DaemonSet) error {
	var pods corev1.PodList

	if err := c.List(context.TODO(), &pods, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(ds.GetLabels()),
		Namespace:     common.FileIntegrityNamespace,
	}); err != nil {
		return err
	}

	for _, pod := range pods.Items {
		err := c.Delete(context.TODO(), &pod, &client.DeleteOptions{})
		if err != nil {
			return err
		}
	}

	return nil
}
