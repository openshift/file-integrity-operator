package fileintegrity

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	controllerruntime "sigs.k8s.io/controller-runtime"

	"github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/controller/metrics"

	"k8s.io/apimachinery/pkg/labels"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/go-logr/logr"

	"github.com/openshift/file-integrity-operator/pkg/common"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var controllerFileIntegritylog = logf.Log.WithName("controller_fileintegrity")

// Add creates a new FileIntegrity Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddFileIntegrityController(mgr manager.Manager, met *metrics.Metrics) error {
	return addFileIntegrityController(mgr, newFileIntegrityReconciler(mgr, met))
}

// newReconciler returns a new reconcile.Reconciler
func newFileIntegrityReconciler(mgr manager.Manager, met *metrics.Metrics) reconcile.Reconciler {
	return &FileIntegrityReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Metrics: met}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func addFileIntegrityController(mgr manager.Manager, r reconcile.Reconciler) error {
	mapper := &fileIntegrityMapper{Client: mgr.GetClient()}
	return controllerruntime.NewControllerManagedBy(mgr).
		Named("fileintegrity-controller").
		For(&v1alpha1.FileIntegrity{}).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(mapper.Map)).
		Complete(r)
}

// blank assignment to verify that FileIntegrityReconciler implements reconcile.Reconciler
var _ reconcile.Reconciler = &FileIntegrityReconciler{}

// handleDefaultConfigMaps creates the inital configMaps needed by the operator and aide pods. It returns the
// active AIDE configuration configMap
func (r *FileIntegrityReconciler) handleDefaultConfigMaps(logger logr.Logger, f *v1alpha1.FileIntegrity) (*corev1.ConfigMap, bool, error) {
	var scriptsUpdated bool
	scriptCm := &corev1.ConfigMap{}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{
		Name:      common.AideReinitScriptConfigMapName,
		Namespace: common.FileIntegrityNamespace,
	}, scriptCm); err != nil {
		if !kerr.IsNotFound(err) {
			return nil, scriptsUpdated, err
		}
		// does not exist, create
		if err := r.Client.Create(context.TODO(), aideReinitScript()); err != nil {
			return nil, scriptsUpdated, err
		}
	} else if ok := dataMatchesReinitScript(scriptCm); !ok {
		// The script data is outdated because of a manual change or an operator update, so we need to restore it.
		logger.Info("re-init script configMap has changed, restoring")
		if err := r.Client.Update(context.TODO(), aideReinitScript()); err != nil {
			return nil, scriptsUpdated, err
		}
		scriptsUpdated = true
	}

	pauseCm := &corev1.ConfigMap{}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{
		Name:      common.PauseConfigMapName,
		Namespace: common.FileIntegrityNamespace,
	}, pauseCm); err != nil {
		if !kerr.IsNotFound(err) {
			return nil, scriptsUpdated, err
		}
		// does not exist, create
		if err := r.Client.Create(context.TODO(), aidePauseScript()); err != nil {
			return nil, scriptsUpdated, err
		}
	} else if ok := dataMatchesPauseScript(pauseCm); !ok {
		// The script data is outdated because of a manual change or an operator update, so we need to restore it.
		logger.Info("holdoff script configMap has changed, restoring")
		if err := r.Client.Update(context.TODO(), aidePauseScript()); err != nil {
			return nil, scriptsUpdated, err
		}
		scriptsUpdated = true
	}

	confCm := &corev1.ConfigMap{}
	if err := r.Client.Get(context.TODO(), types.NamespacedName{
		Name:      f.Name,
		Namespace: common.FileIntegrityNamespace,
	}, confCm); err != nil {
		if !kerr.IsNotFound(err) {
			return nil, scriptsUpdated, err
		}
		// does not exist, create
		if err := r.Client.Create(context.TODO(), defaultAIDEConfigMap(f.Name)); err != nil {
			return nil, scriptsUpdated, err
		}
		return nil, scriptsUpdated, nil
	} else {
		_, hasData := confCm.Data[common.DefaultConfDataKey]
		_, hasOwner := confCm.Labels[common.IntegrityOwnerLabelKey]
		if !hasData || !hasOwner {
			// we had the configMap but its data or owner label was missing, so restore it.
			if err := r.Client.Update(context.TODO(), defaultAIDEConfigMap(f.Name)); err != nil {
				return nil, scriptsUpdated, err
			}
			return nil, scriptsUpdated, nil
		}
	}

	return confCm, scriptsUpdated, nil
}

// // GetFailedNodes from FileIntegrity instance
func (r *FileIntegrityReconciler) GetFailedNodes(fi *v1alpha1.FileIntegrity) ([]string, error) {
	var failedNodes []string
	// get nodestatus objects for the instance
	nodeStatusList := &v1alpha1.FileIntegrityNodeStatusList{}
	listOpts := client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{common.IntegrityOwnerLabelKey: fi.Name}),
	}
	err := r.Client.List(context.TODO(), nodeStatusList, &listOpts)
	if err != nil {
		// if not found, return empty list
		if kerr.IsNotFound(err) {
			return failedNodes, nil
		}
		return failedNodes, err
	}
	for _, nodeStatus := range nodeStatusList.Items {
		if nodeStatus.LastResult.Condition == v1alpha1.NodeConditionFailed {
			failedNodes = append(failedNodes, nodeStatus.NodeName)
		}
	}
	return failedNodes, nil
}

// narrowNodeSelector returns a LabelSelector as-is from instance if node == nil, otherwise, return a LabelSelector for
// node if node is contained in the node group. Used for making a node-specific DS.
func (r *FileIntegrityReconciler) narrowNodeSelector(instance *v1alpha1.FileIntegrity, node *corev1.Node) (metav1.LabelSelector, error) {
	narrowSelector := metav1.LabelSelector{}
	if node == nil {
		narrowSelector.MatchLabels = instance.Spec.NodeSelector
		return narrowSelector, nil
	}

	hostnameFromLabel, ok := node.Labels["kubernetes.io/hostname"]
	if !ok || len(hostnameFromLabel) == 0 {
		return narrowSelector, errors.New("couldn't find hostname label from node")
	}

	narrowSelector.MatchLabels = map[string]string{
		"kubernetes.io/hostname": hostnameFromLabel,
	}

	return narrowSelector, nil
}

// createReinitDaemonSet creates a re-init daemonSet. The daemonSet will be a single-node reinit if node is not "" (like
// in the node update case), otherwise, it will apply to the whole node instance node group (like in the manual re-init
// case).
func (r *FileIntegrityReconciler) createReinitDaemonSet(instance *v1alpha1.FileIntegrity, node, operatorImage string) error {
	daemonSet := &appsv1.DaemonSet{}
	dsName := common.ReinitDaemonSetNodeName(instance.Name, node)
	dsNamespace := common.FileIntegrityNamespace

	getErr := r.Client.Get(context.TODO(), types.NamespacedName{Name: dsName, Namespace: dsNamespace}, daemonSet)
	if getErr == nil {
		// Exists, so continue.
		return nil
	}
	if !kerr.IsNotFound(getErr) {
		return getErr
	}

	reinitNode, tryNodeErr := r.tryGettingNode(node)
	if tryNodeErr != nil && !kerr.IsNotFound(tryNodeErr) {
		return tryNodeErr
	}
	if kerr.IsNotFound(tryNodeErr) {
		// We wanted a node, but it was not found. It might be scaled down. Return with no error so we don't continue with
		// creating a re-init daemonSet.
		return nil
	}

	// The reinit daemonset may need to apply to a single node only.
	selectorOverride, narrowErr := r.narrowNodeSelector(instance, reinitNode)
	if narrowErr != nil {
		return narrowErr
	}

	ds := reinitAideDaemonset(common.ReinitDaemonSetNodeName(instance.Name, node), instance, selectorOverride, operatorImage)
	if err := controllerutil.SetControllerReference(instance, ds, r.Scheme); err != nil {
		return err
	}

	createErr := r.Client.Create(context.TODO(), ds)
	if createErr == nil {
		r.Metrics.IncFileIntegrityReinitDaemonsetUpdate()
	}

	return createErr
}

// If node == "", return nil, nil, otherwise try to find a node with that name. Not found needs to be handled outside
// of this function.
func (r *FileIntegrityReconciler) tryGettingNode(node string) (*corev1.Node, error) {
	if len(node) == 0 {
		return nil, nil
	}
	n := corev1.Node{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: node}, &n)
	if err != nil {
		return nil, err
	}
	return &n, nil
}
func (r *FileIntegrityReconciler) updateAideConfig(conf *corev1.ConfigMap, data, node string, nodeUpdate bool) error {
	confCopy := conf.DeepCopy()
	confCopy.Data[common.DefaultConfDataKey] = data

	if nodeUpdate {
		// Mark the configMap as updated by the user-provided config, for the configMap-controller to trigger an update.
		// check if there is already an annotation for the node if not add it to the list
		if confCopy.Annotations == nil {
			confCopy.Annotations = map[string]string{}
		}
		if nodeListString, ok := confCopy.Annotations[common.AideConfigUpdatedAnnotationKey]; !ok {
			confCopy.Annotations[common.AideConfigUpdatedAnnotationKey] = node
		} else {
			// check if the node is already in the list
			nodeList := strings.Split(nodeListString, ",")
			for _, n := range nodeList {
				if n == node {
					return nil
				}
			}
			nodeList = append(nodeList, node)
			confCopy.Annotations[common.AideConfigUpdatedAnnotationKey] = strings.Join(nodeList, ",")
		}
	}

	return r.Client.Update(context.TODO(), confCopy)
}

func (r *FileIntegrityReconciler) retrieveAndAnnotateAideConfig(conf *corev1.ConfigMap, node string) error {
	cachedConf := &corev1.ConfigMap{}
	// Get the latest config...
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: conf.Name, Namespace: conf.Namespace}, cachedConf)
	if err != nil {
		return err
	}

	return r.updateAideConfig(cachedConf, cachedConf.Data[common.DefaultConfDataKey], node, true)
}

func (r *FileIntegrityReconciler) aideConfigIsDefault(instance *v1alpha1.FileIntegrity) (bool, error) {
	defaultConfigMap := defaultAIDEConfigMap(instance.Name)
	currentConfigMap := &corev1.ConfigMap{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{
		Name:      defaultConfigMap.Name,
		Namespace: defaultConfigMap.Namespace,
	}, currentConfigMap)
	if err != nil {
		return false, err
	}

	return configMapKeyDataMatches(currentConfigMap, defaultConfigMap, common.DefaultConfDataKey), nil
}

// reconcileUserConfig checks if the user provided a configuration of their own and prepares it. Returns true if a new
// configuration was added, false if not.
func (r *FileIntegrityReconciler) reconcileUserConfig(instance *v1alpha1.FileIntegrity,
	reqLogger logr.Logger, currentConfig *corev1.ConfigMap) (bool, error) {
	if len(instance.Spec.Config.Name) == 0 || len(instance.Spec.Config.Namespace) == 0 {
		hasDefaultConfig, err := r.aideConfigIsDefault(instance)
		if err != nil {
			return false, err
		}
		if !hasDefaultConfig {
			// The configuration was previously replaced. We want to restore it now.
			reqLogger.Info("Restoring the AIDE configuration defaults.")
			if err := r.updateAideConfig(currentConfig, DefaultAideConfig, "", false); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, nil
	}

	reqLogger.Info("reconciling user-provided configMap")

	userConfigMap := &corev1.ConfigMap{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{
		Name:      instance.Spec.Config.Name,
		Namespace: instance.Spec.Config.Namespace,
	}, userConfigMap)
	if err != nil {
		if !kerr.IsNotFound(err) {
			reqLogger.Error(err, "error getting aide config configMap")
			return false, err
		}
		// FIXME(jaosorior): This should probably be an error instead
		reqLogger.Info(fmt.Sprintf("warning: user-specified configMap %s/%s does not exist",
			instance.Spec.Config.Namespace, instance.Spec.Config.Name))
		return false, nil
	}

	key := common.DefaultConfDataKey
	if instance.Spec.Config.Key != "" {
		key = instance.Spec.Config.Key
	}

	conf, ok := userConfigMap.Data[key]
	if !ok || len(conf) == 0 {
		reqLogger.Info(fmt.Sprintf("warning: user-specified configMap %s/%s does not have data '%s'",
			instance.Spec.Config.Namespace, instance.Spec.Config.Name, key))
		return false, nil
	}

	preparedConf, err := prepareAideConf(conf)
	if err != nil {
		return false, err
	}

	// Config is the same - we're done
	if preparedConf == currentConfig.Data[common.DefaultConfDataKey] {
		return false, nil
	}

	if err := r.updateAideConfig(currentConfig, preparedConf, "", false); err != nil {
		return false, err
	}

	return true, nil
}

// gets the image of the first container in the operator deployment spec. We expect this to be the deployment named
// file-integrity-operator in the openshift-file-integrity namespace.
func (r *FileIntegrityReconciler) getOperatorDeploymentImage() (string, error) {
	operatorDeployment := &appsv1.Deployment{}
	image := ""
	if err := r.Client.Get(context.TODO(), types.NamespacedName{Name: "file-integrity-operator", Namespace: common.FileIntegrityNamespace}, operatorDeployment); err != nil {
		return "", err
	}
	if len(operatorDeployment.Spec.Template.Spec.Containers) > 0 {
		image = operatorDeployment.Spec.Template.Spec.Containers[0].Image
	}
	return image, nil
}

// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
// Reconcile handles the creation and update of configMaps as well as the initial daemonSets for the AIDE pods.
func (r *FileIntegrityReconciler) FileIntegrityControllerReconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := controllerFileIntegritylog.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("reconciling FileIntegrity")

	// Fetch the FileIntegrity instance
	instance := &v1alpha1.FileIntegrity{}
	err := r.Client.Get(context.TODO(), request.NamespacedName, instance)
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

	// Get the operator image to later set as the daemonSet image. They need to always match before we deprecate RELATED_IMAGE_OPERATOR.
	operatorImage, err := r.getOperatorDeploymentImage()
	if err != nil {
		return reconcile.Result{}, err
	}

	daemonSetName := common.DaemonSetName(instance.Name)

	defaultAideConf, scriptsUpdated, err := r.handleDefaultConfigMaps(reqLogger, instance)
	if err != nil {
		reqLogger.Error(err, "error handling default configMaps")
		return reconcile.Result{}, err
	}
	if defaultAideConf == nil {
		// this just got created, so we should re-queue in order to handle the user provided config next go around.
		return reconcile.Result{Requeue: true}, nil
	}

	// handle user-provided configMap
	hasNewConfig, err := r.reconcileUserConfig(instance, reqLogger, defaultAideConf)
	if err != nil {
		return reconcile.Result{}, err
	}

	// check if we have AideDatabaseReinitOnFailedAnnotationKey annotation
	// if we do, we need to reinit the failed node
	if instance.Annotations != nil {
		if _, ok := instance.Annotations[common.AideDatabaseReinitOnFailedAnnotationKey]; ok {
			// get list of nodestatuses in failed state
			// and add the node to the reinit annotation
			nodes, err := r.GetFailedNodes(instance)
			if err != nil {
				reqLogger.Error(err, "error getting failed nodes")
				return reconcile.Result{}, err
			}
			updatedAnn, needChange := common.GetAddedNodeReinitAnnotation(instance, nodes)
			instanceCopy := instance.DeepCopy()

			if needChange {
				instanceCopy.Annotations = updatedAnn
				reqLogger.Info("Node " + nodes[0] + " added to the reinit annotation")
			}
			reqLogger.Info("Removing reinit on failed annotation")
			delete(instanceCopy.Annotations, common.AideDatabaseReinitOnFailedAnnotationKey)
			if err := r.Client.Update(context.TODO(), instanceCopy); err != nil {
				reqLogger.Error(err, "error updating FileIntegrity annotation for reinit")
				return reconcile.Result{}, err
			}
			return reconcile.Result{}, nil
		}
	}

	nodeToReinit := ""
	nodesToReinit, forceReinit, reinitAll := common.HasReinitAnnotation(instance)
	if forceReinit && !reinitAll {
		nodeToReinit = nodesToReinit[0]
	}

	if hasNewConfig || forceReinit {
		if err := r.createReinitDaemonSet(instance, nodeToReinit, operatorImage); err != nil {
			return reconcile.Result{}, err
		}
		if forceReinit {
			reqLogger.Info("re-init daemonSet created, triggered by demand or nodes", "nodes", nodeToReinit)
			r.Metrics.IncFileIntegrityReinitByDemand()
		} else {
			reqLogger.Info("re-init daemonSet created, triggered by configuration change")
			r.Metrics.IncFileIntegrityReinitByConfig()
		}
	}

	// Remove re-init annotation
	if forceReinit {
		reqLogger.Info("Annotating AIDE config to be updated.")
		if err := r.retrieveAndAnnotateAideConfig(defaultAideConf, nodeToReinit); err != nil {
			return reconcile.Result{}, err
		}
		fiCopy := instance.DeepCopy()
		needChange := false
		fiCopy.Annotations, needChange = common.GetRemovedNodeReinitAnnotation(fiCopy, nodeToReinit)

		if needChange {
			reqLogger.Info("Removing re-init annotation.")
			if err := r.Client.Update(context.TODO(), fiCopy); err != nil {
				reqLogger.Info("Re-init annotation failed to be removed, re-queueing")
				return reconcile.Result{}, nil
			}
		}
	}

	daemonSet := &appsv1.DaemonSet{}
	err = r.Client.Get(context.TODO(), types.NamespacedName{Name: daemonSetName, Namespace: common.FileIntegrityNamespace}, daemonSet)
	if err != nil {
		if !kerr.IsNotFound(err) {
			reqLogger.Error(err, "error getting daemonSet")
			return reconcile.Result{}, err
		}

		if legacyDeleteErr := r.deleteLegacyDaemonSets(instance); legacyDeleteErr != nil {
			return reconcile.Result{}, legacyDeleteErr
		}

		// Check if we're past the initial delay timer by evaluating
		// the time since creation.
		delayTime := time.Duration(instance.Spec.Config.InitialDelay) * time.Second
		shouldScheduleAt := instance.CreationTimestamp.Time.Add(delayTime)

		if time.Now().Before(shouldScheduleAt) {
			s := fmt.Sprintf("Re-queuing request for %s seconds for initial delay", delayTime)
			reqLogger.Info(s)
			return reconcile.Result{Requeue: true, RequeueAfter: delayTime}, nil
		}

		reqLogger.Info("Creating daemonSet", "DaemonSet", daemonSetName)

		ds := aideDaemonset(daemonSetName, instance, operatorImage)

		if ownerErr := controllerutil.SetControllerReference(instance, ds, r.Scheme); ownerErr != nil {
			controllerFileIntegritylog.Error(ownerErr, "Failed to set daemonset ownership", "DaemonSet", ds)
			return reconcile.Result{}, err
		}

		createErr := r.Client.Create(context.TODO(), ds)
		if createErr != nil && !kerr.IsAlreadyExists(createErr) {
			reqLogger.Error(createErr, "error creating daemonSet")
			return reconcile.Result{}, createErr
		}
		if !kerr.IsAlreadyExists(createErr) {
			r.Metrics.IncFileIntegrityDaemonsetUpdate()
		}
	} else {
		dsCopy := daemonSet.DeepCopy()
		argsNeedUpdate := updateDSArgs(dsCopy, instance, reqLogger)
		imgNeedsUpdate := updateDSImage(dsCopy, operatorImage, reqLogger)
		nsNeedsUpdate := updateDSNodeSelector(dsCopy, instance, reqLogger)
		tolsNeedsUpdate := updateDSTolerations(dsCopy, instance, reqLogger)
		volsNeedUpdate := updateDSContainerVolumes(dsCopy, instance, operatorImage, reqLogger)

		if argsNeedUpdate || imgNeedsUpdate || nsNeedsUpdate || tolsNeedsUpdate || volsNeedUpdate || scriptsUpdated {
			if err := r.Client.Update(context.TODO(), dsCopy); err != nil {
				return reconcile.Result{}, err
			}

			r.Metrics.IncFileIntegrityDaemonsetUpdate()

			// TODO: We might want to change this to something that signals to the daemonSet pods that they need to
			// gracefully exit, and let them restart that way.
			err := common.RestartFileIntegrityDs(r.Client, common.DaemonSetName(instance.Name))
			if err != nil {
				return reconcile.Result{}, err
			}
			reqLogger.Info("FileIntegrity daemon configuration changed - pods restarted.")
			r.Metrics.IncFileIntegrityDaemonsetPodKill()
		}
	}
	return reconcile.Result{}, nil
}

// The old daemonSets had a "aide-ds-" prefix, but that is no longer. If any are around after upgrade, delete them.
func (r *FileIntegrityReconciler) deleteLegacyDaemonSets(instance *v1alpha1.FileIntegrity) error {
	daemonSetList := &appsv1.DaemonSetList{}
	if err := r.Client.List(context.TODO(), daemonSetList, &client.ListOptions{LabelSelector: labels.SelectorFromSet(labels.Set{
		common.IntegrityOwnerLabelKey: instance.Name,
	})}); err != nil {
		return err
	}
	for i, _ := range daemonSetList.Items {
		daemonSet := &daemonSetList.Items[i]
		// Check for the old prefixed ds, delete it (it's being replaced by the newly named ones.)
		if strings.HasPrefix(daemonSet.Name, "aide-ds-") {
			if deleteErr := r.Client.Delete(context.TODO(), daemonSet); deleteErr != nil {
				return deleteErr
			}
		}
	}
	return nil
}

func updateDSNodeSelector(currentDS *appsv1.DaemonSet, fi *v1alpha1.FileIntegrity, logger logr.Logger) bool {
	nsRef := &currentDS.Spec.Template.Spec.NodeSelector
	expectedNS := fi.Spec.NodeSelector
	needsUpdate := !reflect.DeepEqual(*nsRef, expectedNS)
	if needsUpdate {
		logger.Info("FileIntegrity needed nodeSelector update")
		*nsRef = expectedNS
	}
	return needsUpdate
}

func updateDSTolerations(currentDS *appsv1.DaemonSet, fi *v1alpha1.FileIntegrity, logger logr.Logger) bool {
	tRef := &currentDS.Spec.Template.Spec.Tolerations
	expectedTolerations := fi.Spec.Tolerations
	needsUpdate := !reflect.DeepEqual(*tRef, expectedTolerations)
	if needsUpdate {
		logger.Info("FileIntegrity needed tolerations update")
		*tRef = expectedTolerations
	}
	return needsUpdate
}

// Returns true when the daemon pod args derived from the FileIntegrity object differ from the current DS.
// Returns false if there was no difference.
// If an update is needed, this will update the arguments from the given DaemonSet
func updateDSArgs(currentDS *appsv1.DaemonSet, fi *v1alpha1.FileIntegrity, logger logr.Logger) bool {
	argsRef := &currentDS.Spec.Template.Spec.Containers[0].Args
	expectedArgs := daemonArgs(currentDS.Name, fi)
	needsUpdate := !reflect.DeepEqual(*argsRef, expectedArgs)
	if needsUpdate {
		logger.Info("FileIntegrity needed DaemonSet command-line arguments update")
		*argsRef = expectedArgs
	}
	return needsUpdate
}

// Returns true when the daemon pod volumeMounts differ from the provided (i.e., during update).
// Returns false if there was no difference.
// If an update is needed, this will update the arguments from the given DaemonSet
func updateDSContainerVolumes(currentDS *appsv1.DaemonSet, fi *v1alpha1.FileIntegrity, operatorImage string, logger logr.Logger) bool {
	expected := aideDaemonset(currentDS.Name, fi, operatorImage)
	volumeMountRef := &currentDS.Spec.Template.Spec.Containers[0].VolumeMounts
	volumeMountsNeedUpdate := !reflect.DeepEqual(*volumeMountRef, expected.Spec.Template.Spec.Containers[0].VolumeMounts)
	if volumeMountsNeedUpdate {
		logger.Info("FileIntegrity needed Daemonset container update (volumeMounts)")
		*volumeMountRef = expected.Spec.Template.Spec.Containers[0].VolumeMounts
	}

	return volumeMountsNeedUpdate
}

// Returns true when the daemon pod image differs from the current DS.
// Returns false if there was no difference.
// If an update is needed, this will update the image reference from the given DaemonSet
func updateDSImage(currentDS *appsv1.DaemonSet, operatorImage string, logger logr.Logger) bool {
	currentImgRef := &currentDS.Spec.Template.Spec.Containers[0].Image
	expectedImg := common.GetComponentImage(operatorImage, common.OPERATOR)
	needsUpdate := *currentImgRef != expectedImg
	if needsUpdate {
		logger.Info("FileIntegrity needed image update", "Expected-Image", expectedImg, "Current-Image", currentImgRef)
		*currentImgRef = expectedImg
	}
	return needsUpdate
}

func defaultAIDEConfigMap(name string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: common.FileIntegrityNamespace,
			Labels: map[string]string{
				common.AideConfigLabelKey:     "",
				common.IntegrityOwnerLabelKey: name,
			},
		},
		Data: map[string]string{
			common.DefaultConfDataKey: DefaultAideConfig,
		},
	}
}

func aideReinitScript() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.AideReinitScriptConfigMapName,
			Namespace: common.FileIntegrityNamespace,
		},
		Data: map[string]string{
			common.AideScriptKey: aideReinitContainerScript,
		},
	}
}

func aidePauseScript() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.PauseConfigMapName,
			Namespace: common.FileIntegrityNamespace,
		},
		Data: map[string]string{
			common.AidePauseScriptKey: aidePauseContainerScript,
		},
	}
}

func dataMatchesReinitScript(cm *corev1.ConfigMap) bool {
	return configMapKeyDataMatches(cm, aideReinitScript(), common.AideScriptKey)
}

func dataMatchesPauseScript(cm *corev1.ConfigMap) bool {
	return configMapKeyDataMatches(cm, aidePauseScript(), common.AidePauseScriptKey)
}

func configMapKeyDataMatches(cm1, cm2 *corev1.ConfigMap, key string) bool {
	a := cm1.Data[key]
	b := cm2.Data[key]
	return a == b
}

// reinitAideDaemonset returns a DaemonSet that runs a one-shot pod on each node. This pod touches a file
// on the host OS that informs the AIDE daemon to back up and reinitialize the AIDE db.
func reinitAideDaemonset(reinitDaemonSetName string, fi *v1alpha1.FileIntegrity, selector metav1.LabelSelector, operatorImage string) *appsv1.DaemonSet {
	priv := true
	runAs := int64(0)
	mode := int32(0744)

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      reinitDaemonSetName,
			Namespace: common.FileIntegrityNamespace,
			Labels: map[string]string{
				common.IntegrityReinitOwnerLabelKey: fi.Name,
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": reinitDaemonSetName,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": reinitDaemonSetName,
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector:       selector.MatchLabels,
					Tolerations:        fi.Spec.Tolerations,
					ServiceAccountName: common.OperatorServiceAccountName,
					InitContainers: []corev1.Container{
						{
							SecurityContext: &corev1.SecurityContext{
								Privileged: &priv,
								RunAsUser:  &runAs,
							},
							Name:    "reinit-script",
							Image:   common.GetComponentImage(operatorImage, common.OPERATOR),
							Command: []string{common.AideScriptPath},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "hostroot",
									MountPath: "/hostroot",
								},
								{
									Name:      common.AideReinitScriptConfigMapName,
									MountPath: "/scripts",
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("10Mi"),
									corev1.ResourceCPU:    resource.MustParse("10m"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("50Mi"),
									corev1.ResourceCPU:    resource.MustParse("50m"),
								},
							},
						},
					},
					// make this an endless loop
					Containers: []corev1.Container{
						{
							Name:    "pause-script",
							Command: []string{common.PausePath},
							Image:   common.GetComponentImage(operatorImage, common.OPERATOR),
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      common.PauseConfigMapName,
									MountPath: "/scripts",
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("10Mi"),
									corev1.ResourceCPU:    resource.MustParse("10m"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("50Mi"),
									corev1.ResourceCPU:    resource.MustParse("50m"),
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
						{
							Name: common.AideReinitScriptConfigMapName,
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: common.AideReinitScriptConfigMapName,
									},
									DefaultMode: &mode,
								},
							},
						},
						{
							Name: common.PauseConfigMapName,
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: common.PauseConfigMapName,
									},
									DefaultMode: &mode,
								},
							},
						},
					},
				},
			},
		},
	}
}

func aideDaemonset(dsName string, fi *v1alpha1.FileIntegrity, operatorImage string) *appsv1.DaemonSet {
	priv := true
	runAs := int64(0)
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dsName,
			Namespace: common.FileIntegrityNamespace,
			Labels: map[string]string{
				common.IntegrityOwnerLabelKey: fi.Name,
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": dsName,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":                         dsName,
						common.IntegrityPodLabelKey:   "",
						common.IntegrityOwnerLabelKey: fi.Name,
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector:       fi.Spec.NodeSelector,
					Tolerations:        fi.Spec.Tolerations,
					ServiceAccountName: common.DaemonServiceAccountName,
					Containers: []corev1.Container{
						{
							SecurityContext: &corev1.SecurityContext{
								Privileged: &priv,
								RunAsUser:  &runAs,
							},
							Name:  "daemon",
							Image: common.GetComponentImage(operatorImage, common.OPERATOR),
							Args:  daemonArgs(dsName, fi),
							Env: []corev1.EnvVar{
								{
									Name: "NODE_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "spec.nodeName",
										},
									},
								},
								{
									// Needed for friendlier memory reporting as long as we are on golang < 1.16
									Name:  "GODEBUG",
									Value: "madvdontneed=1",
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "hostroot",
									MountPath: "/hostroot",
								},
								{
									Name:      "config",
									MountPath: "/config",
								},
								{
									Name:      "tmp",
									MountPath: "/tmp",
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("40Mi"),
									corev1.ResourceCPU:    resource.MustParse("40m"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("600Mi"),
									corev1.ResourceCPU:    resource.MustParse("300m"),
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
						{
							// for pprof
							Name: "tmp",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{
									Medium:    corev1.StorageMediumDefault,
									SizeLimit: nil,
								},
							},
						},
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: fi.Name,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func getMaxBackups(fi *v1alpha1.FileIntegrity) string {
	return strconv.Itoa(fi.Spec.Config.MaxBackups)
}

func getGracePeriod(fi *v1alpha1.FileIntegrity) string {
	gracePeriod := fi.Spec.Config.GracePeriod
	if gracePeriod < 10 {
		gracePeriod = 10
	}
	return strconv.Itoa(gracePeriod)
}

func getDebug(fi *v1alpha1.FileIntegrity) string {
	return strconv.FormatBool(fi.Spec.Debug)
}

func daemonArgs(dsName string, fi *v1alpha1.FileIntegrity) []string {
	return []string{"daemon",
		"--lc-file=" + aideLogPath,
		"--lc-config-map-prefix=" + dsName,
		"--owner=" + fi.Name,
		"--namespace=" + fi.Namespace,
		"--interval=" + getGracePeriod(fi),
		"--debug=" + getDebug(fi),
		"--maxbackups=" + getMaxBackups(fi),
		"--aideconfigdir=/config",
		//"--pprof=true",
	}
}
