package node

import (
	"context"
	"fmt"
	controllerruntime "sigs.k8s.io/controller-runtime"

	"github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/controller/metrics"

	"github.com/go-logr/logr"

	"github.com/openshift/file-integrity-operator/pkg/common"
	mcfgconst "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var controllerNodeLog = logf.Log.WithName("controller_node")

// Add creates a new Node Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddNodeController(mgr manager.Manager, met *metrics.Metrics) error {
	return addNodeControllerReconciler(mgr, newNodeControllerReconciler(mgr, met))
}

// newReconciler returns a new reconcile.Reconciler
func newNodeControllerReconciler(mgr manager.Manager, met *metrics.Metrics) reconcile.Reconciler {
	cfg := mgr.GetConfig()
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		panic(fmt.Errorf("Unable to get clientset: %v", err))
	}
	restclient := clientset.CoreV1().RESTClient()
	return &NodeReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), RestClient: restclient, Cfg: cfg, Metrics: met}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func addNodeControllerReconciler(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	return controllerruntime.NewControllerManagedBy(mgr).
		Named("node-controller").
		For(&corev1.Node{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}

// blank assignment to verify that NodeReconciler implements reconcile.Reconciler
var _ reconcile.Reconciler = &NodeReconciler{}

// Reconcile reads that state of the cluster for a Node object and makes changes based on the state read
// and what is in the Node.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *NodeReconciler) NodeControllerReconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := controllerNodeLog.WithValues("Node.Name", request.Name)
	reqLogger.Info("Reconciling Node")

	// Fetch the Node instance
	node := &corev1.Node{}
	err := r.Client.Get(context.TODO(), request.NamespacedName, node)
	if err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	currentConfig := node.Annotations[mcfgconst.CurrentMachineConfigAnnotationKey]
	desiredConfig := node.Annotations[mcfgconst.DesiredMachineConfigAnnotationKey]
	mcdState := node.Annotations[mcfgconst.MachineConfigDaemonStateAnnotationKey]

	// NOTE(jaosorior): If, for some reason, the MCO is not running on a deployment, mcdState
	// will be empty, and this reconciler just won't do anything. This is fine.
	if nodeHasMCOAnnotations(node) {
		fis, err := r.findRelevantFileIntegrities(node)
		if err != nil {
			return reconcile.Result{}, err
		}
		if isNodeBeingUpdateByMCO(currentConfig, desiredConfig, mcdState) {
			reqLogger.Info("Node is currently updating.",
				"currentConfig", currentConfig, "desiredConfig", desiredConfig)
			// An update is about to take place or already taking place
			return reconcile.Result{}, r.addHoldOffAnnotations(reqLogger, fis, node)
		} else if isNodeUpToDateWithMCO(currentConfig, desiredConfig, mcdState) ||
			isNodeDegraded(mcdState) {
			reqLogger.Info(fmt.Sprintf("Node is up-to-date. Degraded: %v", isNodeDegraded(mcdState)),
				"currentConfig", currentConfig, "desiredConfig", desiredConfig,
				"mcdState", mcdState)
			// No update is taking place or it's done already or
			// MCO can't update a host, might as well not hold the integrity checks
			relevantFIs := r.getAnnotatedFileIntegrities(fis, node)
			err := r.removeHoldoffAnnotationAndReinitFileIntegrityDatabases(reqLogger, relevantFIs, node)
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

func (r *NodeReconciler) findRelevantFileIntegrities(currentnode *corev1.Node) ([]*v1alpha1.FileIntegrity, error) {
	resultingFIs := []*v1alpha1.FileIntegrity{}
	fiList := v1alpha1.FileIntegrityList{}
	err := r.Client.List(context.TODO(), &fiList)
	if err != nil {
		return resultingFIs, err
	}
	for fiIdx := range fiList.Items {
		fi := fiList.Items[fiIdx]
		nodeList := corev1.NodeList{}
		listOpts := client.ListOptions{
			LabelSelector: labels.SelectorFromSet(fi.Spec.NodeSelector),
		}

		err = r.Client.List(context.TODO(), &nodeList, &listOpts)
		if err != nil {
			return resultingFIs, err
		}

		for nodeIdx := range nodeList.Items {
			node := nodeList.Items[nodeIdx]
			if node.Name == currentnode.Name {
				resultingFIs = append(resultingFIs, &fi)
			}
		}
	}
	return resultingFIs, nil
}

func (r *NodeReconciler) addHoldOffAnnotations(logger logr.Logger, fis []*v1alpha1.FileIntegrity, n *corev1.Node) error {
	for _, fi := range fis {
		if common.IsNodeInHoldoff(fi, n.Name) {
			continue
		}
		newAnnotation, needsChange := common.GetAddedNodeHoldoffAnnotation(fi, n.Name)
		if !needsChange {
			continue
		}
		ficopy := fi.DeepCopy()
		ficopy.Annotations = newAnnotation
		err := r.Client.Update(context.TODO(), ficopy)
		if err != nil {
			return fmt.Errorf("failed to add node %s to holdoff list: %w", n.Name, err)
		}
		logger.Info("Added Holdoff annotation to FileIntegrity for node", "node", n.Name, "fi", fi.Name)
		r.Metrics.IncFileIntegrityPause(n.Name)
	}
	return nil
}

func (r *NodeReconciler) getAnnotatedFileIntegrities(fis []*v1alpha1.FileIntegrity, n *corev1.Node) []*v1alpha1.FileIntegrity {
	annotatedFIs := []*v1alpha1.FileIntegrity{}
	for _, fi := range fis {
		if common.IsNodeInHoldoff(fi, n.Name) {
			annotatedFIs = append(annotatedFIs, fi)
		}
	}
	return annotatedFIs
}

func (r *NodeReconciler) removeHoldoffAnnotationAndReinitFileIntegrityDatabases(logger logr.Logger, fis []*v1alpha1.FileIntegrity,
	n *corev1.Node) error {
	for _, fi := range fis {
		// Only reinit for FIs that were previously in holdoff.
		if !common.IsNodeInHoldoff(fi, n.Name) {
			continue
		}
		// Skip reinit if the node already has a reinit annotation.
		if common.IsNodeInReinit(fi, n.Name) {
			continue
		}
		newHoldOffAnnotation, needsChange := common.GetRemovedNodeHoldoffAnnotation(fi, n.Name)
		if !needsChange {
			continue
		}
		fi.Annotations = newHoldOffAnnotation
		// Add the reinit annotation
		newReinitAnnotation, needsChange := common.GetAddedNodeReinitAnnotation(fi, []string{n.Name})
		if !needsChange {
			continue
		}
		ficopy := fi.DeepCopy()
		ficopy.Annotations = newReinitAnnotation
		err := r.Client.Update(context.TODO(), ficopy)
		if err != nil {
			return fmt.Errorf("failed to update Fileintegrity %s holdoff and reinit annotations for node %s: %w", fi.Name, n.Name, err)
		}

		logger.Info("Removed Holdoff annotation from FileIntegrity for node", "node", n.Name, "fi", fi.Name)
		logger.Info("Added Reinit annotation to FileIntegrity for node", "node", n.Name, "fi", fi.Name)
		r.Metrics.IncFileIntegrityUnpause(n.Name)
		r.Metrics.IncFileIntegrityReinitByNode(n.Name)

	}
	return nil
}

func nodeHasMCOAnnotations(n *corev1.Node) bool {
	_, hasCurrentConfig := n.Annotations[mcfgconst.CurrentMachineConfigAnnotationKey]
	_, hasDesiredConfig := n.Annotations[mcfgconst.DesiredMachineConfigAnnotationKey]
	_, hasMcdState := n.Annotations[mcfgconst.MachineConfigDaemonStateAnnotationKey]
	return hasCurrentConfig && hasDesiredConfig && hasMcdState
}

// isNodeUpToDateWithMCO describes whether an update is about to take place or
// already taking place
func isNodeBeingUpdateByMCO(currentConfig, desiredConfig, mcdState string) bool {
	return currentConfig != desiredConfig && mcdState == mcfgconst.MachineConfigDaemonStateWorking
}

// isNodeUpToDateWithMCO describes whether no update is taking place or it's
// done already
func isNodeUpToDateWithMCO(currentConfig, desiredConfig, mcdState string) bool {
	return currentConfig == desiredConfig && mcdState == mcfgconst.MachineConfigDaemonStateDone
}

// isNodeDegraded describes if the node is degraded, so the MCO can't update it.
func isNodeDegraded(mcdState string) bool {
	return mcdState == mcfgconst.MachineConfigDaemonStateDegraded
}
