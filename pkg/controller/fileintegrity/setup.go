/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package fileintegrity

import (
	"context"
	"github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/openshift/file-integrity-operator/pkg/controller/metrics"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlLog "sigs.k8s.io/controller-runtime/pkg/log"
)

// FileIntegrityReconciler reconciles a FileIntegrity object
type FileIntegrityReconciler struct {
	client.Client
	*runtime.Scheme
	*metrics.Metrics
}

// These are perms for all controllers.

//+kubebuilder:rbac:groups=core,resources=deployments;pods;services;services/finalizers;endpoints;persistentvolumeclaims;events;configmaps;secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=deployments;daemonsets;replicasets;statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;create;update
//+kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules,verbs=create
//+kubebuilder:rbac:groups=apps,resourceNames=file-integrity-operator,resources=deployments/finalizers,verbs=update
//+kubebuilder:rbac:groups=security.openshift.io,resourceNames=privileged,resources=securitycontextconstraints,verbs=use
//+kubebuilder:rbac:groups=fileintegrity.openshift.io,resources=fileintegritynodestatuses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=fileintegrity.openshift.io,resources=fileintegritynodestatuses/finalizers,verbs=update
//+kubebuilder:rbac:groups=fileintegrity.openshift.io,resources=fileintegrities,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=fileintegrity.openshift.io,resources=fileintegrities/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=fileintegrity.openshift.io,resources=fileintegrities/finalizers,verbs=update
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the FileIntegrity object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.10.0/pkg/reconcile
func (r *FileIntegrityReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = ctrlLog.FromContext(ctx)

	// your logic here
	return r.FileIntegrityControllerReconcile(req)
}

// SetupWithManager sets up the controller with the Manager.
func (r *FileIntegrityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.FileIntegrity{}).
		Complete(r)
}
