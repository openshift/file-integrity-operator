package node

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	fiv1alpha1 "github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/common"
	mcfgconst "github.com/openshift/machine-config-operator/pkg/daemon/constants"
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
)

var log = logf.Log.WithName("controller_node")

const (
	addHoldOffScriptCMName    = "addholdoff"
	removeHoldOffScriptCMName = "rmholdoff"
)

var (
	// Script that makes sure that the holdoff file exists.
	addHoldOffScript = fmt.Sprintf(`#!/bin/bash

	touch %s
	`, common.IntegrityCheckHoldoffFilePath)

	// Script that makes sure that the holdoff file doesn't exist.
	// If the file is not there, we're good. If the file is there
	// we gotta remove it.
	removeHoldOffScript = fmt.Sprintf(`#!/bin/bash

	if [ -f %[1]s ]; then
		rm %[1]s
	fi
	`, common.IntegrityCheckHoldoffFilePath)
)

// Add creates a new Node Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileNode{client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("node-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource Node
	err = c.Watch(&source.Kind{Type: &corev1.Node{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &corev1.Node{},
	})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileNode implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileNode{}

// ReconcileNode reconciles a Node object
type ReconcileNode struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a Node object and makes changes based on the state read
// and what is in the Node.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileNode) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Node.Name", request.Name)
	reqLogger.Info("Reconciling Node")

	// Fetch the Node instance
	node := &corev1.Node{}
	err := r.client.Get(context.TODO(), request.NamespacedName, node)
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
		fis := r.findRelevantFileIntegrities(node)
		if isNodeBeingUpdateByMCO(currentConfig, desiredConfig, mcdState) {
			reqLogger.Info("Node is currently updating.",
				"currentConfig", currentConfig, "desiredConfig", desiredConfig)
			// An update is about to take place or already taking place
			if err := r.addHoldOffAnnotations(fis); err != nil {
				return reconcile.Result{}, err
			}
			return r.reconcileAddHoldOff(node, reqLogger)
		} else if isNodeUpToDateWithMCO(currentConfig, desiredConfig, mcdState) ||
			isNodeDegraded(mcdState) {
			reqLogger.Info(fmt.Sprintf("Node is up-to-date. Degraded: %v", isNodeDegraded(mcdState)),
				"currentConfig", currentConfig, "desiredConfig", desiredConfig,
				"mcdState", mcdState)
			// No update is taking place or it's done already or
			// MCO can't update a host, might as well not hold the integrity checks
			relevantFIs := r.getAnnotatedFileIntegrities(fis)
			if err := r.removeHoldoffAnnotationAndReinitFileIntegrityDatabases(relevantFIs, node); err != nil {
				return reconcile.Result{}, err
			}
			// reinit database && remove holdoff annotation
			return r.reconcileRemoveHoldOff(node, reqLogger)
		}
	}

	return reconcile.Result{}, nil
}

func (r *ReconcileNode) reconcileAddHoldOff(node *corev1.Node, reqLogger logr.Logger) (reconcile.Result, error) {
	if res, err := r.reconcileDeleteWorkloadForNode(removeHoldOffScriptCMName, removeHoldOffScript, node, reqLogger); err != nil {
		return res, err
	}

	return r.reconcileCreateWorkloadForNode(addHoldOffScriptCMName, addHoldOffScript, node, reqLogger)
}

func (r *ReconcileNode) reconcileRemoveHoldOff(node *corev1.Node, reqLogger logr.Logger) (reconcile.Result, error) {
	if res, err := r.reconcileDeleteWorkloadForNode(addHoldOffScriptCMName, addHoldOffScript, node, reqLogger); err != nil {
		return res, err
	}

	return r.reconcileCreateWorkloadForNode(removeHoldOffScriptCMName, removeHoldOffScript, node, reqLogger)
}

func (r *ReconcileNode) reconcileCreateWorkloadForNode(name, script string, node *corev1.Node, reqLogger logr.Logger) (reconcile.Result, error) {
	cmToBeCreated := newGenericHoldOffCM(name, script, node)
	if res, err := r.reconcileCreateObjForNode(cmToBeCreated.Name, cmToBeCreated, reqLogger); err != nil {
		return res, err
	}

	podToBeCreated := newGenericHoldOffIntegrityCheckPod(cmToBeCreated.Name, name, node)
	return r.reconcileCreateObjForNode(podToBeCreated.Name, podToBeCreated, reqLogger)
}

func (r *ReconcileNode) reconcileDeleteWorkloadForNode(name, script string, node *corev1.Node, reqLogger logr.Logger) (reconcile.Result, error) {
	cmToBeDeleted := newGenericHoldOffCM(name, script, node)
	if res, err := r.reconcileDeleteObjForNode(cmToBeDeleted.Name, cmToBeDeleted, reqLogger); err != nil {
		return res, err
	}

	podToBeDeleted := newGenericHoldOffIntegrityCheckPod(cmToBeDeleted.Name, name, node)
	return r.reconcileDeleteObjForNode(podToBeDeleted.Name, podToBeDeleted, reqLogger)
}

func (r *ReconcileNode) reconcileDeleteObjForNode(name string, obj runtime.Object, reqLogger logr.Logger) (reconcile.Result, error) {
	// Check if this object already exists
	err := r.client.Delete(context.TODO(), obj)
	if err != nil && errors.IsNotFound(err) {
		// Object doesn't exist anyway - don't requeue
		return reconcile.Result{}, nil
	} else if err != nil {
		return reconcile.Result{}, err
	}

	// Object deleted successfully - don't requeue
	kind := getKind(obj)
	reqLogger.Info(fmt.Sprintf("%s deleted", kind), "Name", name)
	return reconcile.Result{}, nil
}

func (r *ReconcileNode) reconcileCreateObjForNode(name string, obj runtime.Object, reqLogger logr.Logger) (reconcile.Result, error) {
	// Check if this object already exists
	found := obj.DeepCopyObject()
	err := r.client.Get(context.TODO(), types.NamespacedName{Name: name, Namespace: common.FileIntegrityNamespace}, found)
	if err != nil && errors.IsNotFound(err) {
		kind := getKind(obj)
		err = r.client.Create(context.TODO(), obj)
		if err != nil {
			return reconcile.Result{}, err
		}
		reqLogger.Info(fmt.Sprintf("%s created", kind), "Name", name)

		// Object created successfully - don't requeue
		return reconcile.Result{}, nil
	} else if err != nil {
		return reconcile.Result{}, err
	}

	// Object already exists - don't requeue
	return reconcile.Result{}, nil
}

func (r *ReconcileNode) findRelevantFileIntegrities(currentnode *corev1.Node) []*fiv1alpha1.FileIntegrity {
	resultingFIs := []*fiv1alpha1.FileIntegrity{}
	fiList := fiv1alpha1.FileIntegrityList{}
	r.client.List(context.TODO(), &fiList)
	for _, fi := range fiList.Items {
		nodeList := corev1.NodeList{}
		listOpts := client.ListOptions{
			LabelSelector: labels.SelectorFromSet(fi.Spec.NodeSelector),
		}
		r.client.List(context.TODO(), &nodeList, &listOpts)
		for _, node := range nodeList.Items {
			if node.Name == currentnode.Name {
				resultingFIs = append(resultingFIs, &fi)
			}
		}
	}
	return resultingFIs
}

func (r *ReconcileNode) addHoldOffAnnotations(fis []*fiv1alpha1.FileIntegrity) error {
	for _, fi := range fis {
		fiCopy := fi.DeepCopy()
		if fiCopy.Annotations == nil {
			fiCopy.Annotations = map[string]string{}
		}

		fiCopy.Annotations[common.IntegrityHoldoffAnnotationKey] = ""
		if err := r.client.Update(context.TODO(), fiCopy); err != nil {
			return err
		}
	}
	return nil
}

func (r *ReconcileNode) getAnnotatedFileIntegrities(fis []*fiv1alpha1.FileIntegrity) []*fiv1alpha1.FileIntegrity {
	annotatedFIs := []*fiv1alpha1.FileIntegrity{}
	for _, fi := range fis {
		if fi.Annotations == nil {
			continue
		}

		_, found := fi.Annotations[common.IntegrityHoldoffAnnotationKey]
		if found {
			annotatedFIs = append(annotatedFIs, fi)
		}
	}
	return annotatedFIs
}

func (r *ReconcileNode) removeHoldoffAnnotationAndReinitFileIntegrityDatabases(fis []*fiv1alpha1.FileIntegrity, node *corev1.Node) error {
	for _, fi := range fis {
		// Only reinit for FIs that were previously in holdoff.
		if _, ok := fi.Annotations[common.IntegrityHoldoffAnnotationKey]; ok {
			fiCopy := fi.DeepCopy()
			fiCopy.Annotations[common.AideDatabaseReinitAnnotationKey] = ""
			fiCopy.Annotations[common.AideDatabaseReinitNodeAnnotationKey] = node.Name
			delete(fiCopy.Annotations, common.IntegrityHoldoffAnnotationKey)
			if err := r.client.Update(context.TODO(), fiCopy); err != nil {
				return err
			}
		}
	}
	return nil
}

// This creates a new generic CM that contains the script that adds/removes the
// hold-off file for the integrity checks.
func newGenericHoldOffCM(name, script string, node *corev1.Node) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.Name + "-" + name,
			Namespace: common.FileIntegrityNamespace,
			Labels: map[string]string{
				common.IntegrityCMLabelKey: "",
			},
		},
		Data: map[string]string{
			name: script,
		},
	}
}

func newGenericHoldOffIntegrityCheckPod(holdoffCmName, holdoffScriptName string, node *corev1.Node) *corev1.Pod {
	priv := true
	runAs := int64(0)
	mode := int32(0744)
	podLabels := map[string]string{
		common.IntegrityPodLabelKey: "",
	}

	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      holdoffCmName,
			Namespace: common.FileIntegrityNamespace,
			Labels:    podLabels,
		},
		Spec: corev1.PodSpec{
			// Schedule directly to the node (skip the scheduler)
			NodeName: node.Name,
			Tolerations: []corev1.Toleration{
				{
					Key:      "node-role.kubernetes.io/master",
					Operator: "Exists",
					Effect:   "NoSchedule",
				},
			},
			ServiceAccountName: common.OperatorServiceAccountName,
			RestartPolicy:      corev1.RestartPolicyOnFailure,
			Containers: []corev1.Container{
				{
					SecurityContext: &corev1.SecurityContext{
						Privileged: &priv,
						RunAsUser:  &runAs,
					},
					Name: "integrity-holdoff",
					// FIXME(jaosorior): Can we use UBI instead?
					Image:   common.GetComponentImage(common.AIDE),
					Command: []string{"/scripts/" + holdoffScriptName},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "hostroot",
							MountPath: "/hostroot",
						},
						{
							Name:      "holdoffscript",
							MountPath: "/scripts",
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
					Name: "holdoffscript",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: holdoffCmName,
							},
							DefaultMode: &mode,
						},
					},
				},
			},
		},
	}
}

func getKind(obj runtime.Object) string {
	kind := obj.GetObjectKind()
	gvk := kind.GroupVersionKind()
	return gvk.Kind
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
