package fileintegrity

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	fileintegrityv1alpha1 "github.com/mrogers950/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"github.com/mrogers950/file-integrity-operator/pkg/common"
)

var log = logf.Log.WithName("controller_fileintegrity")

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new FileIntegrity Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileFileIntegrity{client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("fileintegrity-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource FileIntegrity
	err = c.Watch(&source.Kind{Type: &fileintegrityv1alpha1.FileIntegrity{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileFileIntegrity implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileFileIntegrity{}

// ReconcileFileIntegrity reconciles a FileIntegrity object
type ReconcileFileIntegrity struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
}

// handleDefaultConfigMaps creates the inital configMaps needed by the operator and aide pods. It returns the
// active AIDE configuration configMap
func (r *ReconcileFileIntegrity) handleDefaultConfigMaps() (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{
		Name:      common.AideScriptConfigMapName,
		Namespace: common.FileIntegrityNamespace,
	}, cm); err != nil {
		if !kerr.IsNotFound(err) {
			return nil, err
		}
		// does not exist, create
		if err := r.client.Create(context.TODO(), defaultAIDEScript()); err != nil {
			return nil, err
		}
	}

	if err := r.client.Get(context.TODO(), types.NamespacedName{
		Name:      common.AideInitScriptConfigMapName,
		Namespace: common.FileIntegrityNamespace,
	}, cm); err != nil {
		if !kerr.IsNotFound(err) {
			return nil, err
		}
		// does not exist, create
		if err := r.client.Create(context.TODO(), aideInitScript()); err != nil {
			return nil, err
		}
	}

	if err := r.client.Get(context.TODO(), types.NamespacedName{
		Name:      common.AideReinitScriptConfigMapName,
		Namespace: common.FileIntegrityNamespace,
	}, cm); err != nil {
		if !kerr.IsNotFound(err) {
			return nil, err
		}
		// does not exist, create
		if err := r.client.Create(context.TODO(), aideReinitScript()); err != nil {
			return nil, err
		}
	}

	if err := r.client.Get(context.TODO(), types.NamespacedName{
		Name:      common.DefaultConfigMapName,
		Namespace: common.FileIntegrityNamespace,
	}, cm); err != nil {
		if !kerr.IsNotFound(err) {
			return nil, err
		}
		// does not exist, create
		if err := r.client.Create(context.TODO(), defaultAIDEConfigMap()); err != nil {
			return nil, err
		}
	} else if _, ok := cm.Data[common.DefaultConfDataKey]; !ok {
		// we had the configMap but its data was missing for some reason, so restore it.
		if err := r.client.Update(context.TODO(), defaultAIDEConfigMap()); err != nil {
			return nil, err
		}
	}

	return cm, nil
}

// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
// Reconcile handles the creation and update of configMaps as well as the initial daemonSets for the AIDE pods.
func (r *ReconcileFileIntegrity) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("reconciling FileIntegrity")

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

	defaultAideConf, err := r.handleDefaultConfigMaps()
	if err != nil {
		reqLogger.Error(err, "error handling default configMaps")
		return reconcile.Result{}, err
	}
	if defaultAideConf == nil {
		// this just got created, so we should re-queue in order to handle the user provided config next go around.
		return reconcile.Result{Requeue: true}, nil
	}

	// handle user-provided configmap
	reqLogger.Info("instance spec", "Instance.Spec", instance.Spec)
	if len(instance.Spec.Config.Name) > 0 && len(instance.Spec.Config.Namespace) > 0 {
		reqLogger.Info("checking for configmap update")

		cm := &corev1.ConfigMap{}
		cfErr := r.client.Get(context.TODO(), types.NamespacedName{Name: instance.Spec.Config.Name, Namespace: instance.Spec.Config.Namespace}, cm)
		if cfErr != nil {
			if !kerr.IsNotFound(cfErr) {
				reqLogger.Error(cfErr, "error getting aide config configmap")
				return reconcile.Result{}, cfErr
			}
		}

		if !kerr.IsNotFound(cfErr) {
			key := common.DefaultConfDataKey
			if instance.Spec.Config.Key != "" {
				key = instance.Spec.Config.Key
			}
			conf, ok := cm.Data[key]
			if ok && len(conf) > 0 {
				preparedConf, prepErr := prepareAideConf(conf)
				if prepErr != nil {
					reqLogger.Error(prepErr, "error preparing provided aide conf")
					return reconcile.Result{}, prepErr
				}
				// the converted config is different than the currently installed config - update it
				// TODO - refactor this
				if preparedConf != defaultAideConf.Data[common.DefaultConfDataKey] {
					reqLogger.Info("updating aide conf")
					defaultAideConfCopy := defaultAideConf.DeepCopy()
					defaultAideConfCopy.Data[common.DefaultConfDataKey] = preparedConf
					// mark the configMap as updated by the user-provided config, for the
					// configmap-controller to trigger a rolling update.
					annotations := map[string]string{}
					if defaultAideConfCopy.Annotations == nil {
						defaultAideConfCopy.Annotations = annotations
					}
					defaultAideConfCopy.Annotations["fileintegrity.openshift.io/updated"] = "true"

					updateErr := r.client.Update(context.TODO(), defaultAideConfCopy)
					if updateErr != nil {
						reqLogger.Error(updateErr, "error updating default configmap")
						return reconcile.Result{}, updateErr
					}
					// create the daemonSets for the re-initialize pods.
					daemonSet := &appsv1.DaemonSet{}
					err = r.client.Get(context.TODO(), types.NamespacedName{
						Name:      common.ReinitDaemonSetName,
						Namespace: common.FileIntegrityNamespace,
					}, daemonSet)
					if err != nil {
						if !kerr.IsNotFound(err) {
							reqLogger.Error(err, "error getting reinit daemonSet")
							return reconcile.Result{}, err
						}
						// create
						ds := reinitAideDaemonset()
						createErr := r.client.Create(context.TODO(), ds)
						if createErr != nil {
							reqLogger.Error(createErr, "error creating reinit daemonSet")
							return reconcile.Result{}, createErr
						}
					}
				}
			}
		}
	}

	reqLogger.Info("reconciling daemonSets")
	daemonSet := &appsv1.DaemonSet{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: common.DaemonSetName, Namespace: common.FileIntegrityNamespace}, daemonSet)
	if err != nil {
		if !kerr.IsNotFound(err) {
			reqLogger.Error(err, "error getting daemonSet")
			return reconcile.Result{}, err
		}
		// create
		ds := aideDaemonset()
		createErr := r.client.Create(context.TODO(), ds)
		if createErr != nil {
			reqLogger.Error(createErr, "error creating daemonSet")
			return reconcile.Result{}, createErr
		}
	}
	return reconcile.Result{}, nil
}

func defaultAIDEConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.DefaultConfigMapName,
			Namespace: common.FileIntegrityNamespace,
		},
		Data: map[string]string{
			"aide.conf": defaultAideConfig,
		},
	}
}

func defaultAIDEScript() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.AideScriptConfigMapName,
			Namespace: common.FileIntegrityNamespace,
		},
		Data: map[string]string{
			"aide.sh": aideScript,
		},
	}
}

func aideInitScript() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.AideInitScriptConfigMapName,
			Namespace: common.FileIntegrityNamespace,
		},
		Data: map[string]string{
			"aide.sh": aideInitContainerScript,
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
			"aide.sh": aideReinitContainerScript,
		},
	}
}

// reinitAideDaemonset returns a DaemonSet that runs a one-shot pod on each node. This pod touches a file
// on the host OS that informs the AIDE init container script to back up and reinitialize the AIDE db.
func reinitAideDaemonset() *appsv1.DaemonSet {
	priv := true
	runAs := int64(0)
	mode := int32(0744)

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.ReinitDaemonSetName,
			Namespace: common.FileIntegrityNamespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": common.ReinitDaemonSetName,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": common.ReinitDaemonSetName,
					},
				},
				Spec: corev1.PodSpec{
					Tolerations: []corev1.Toleration{
						{
							Key:      "node-role.kubernetes.io/master",
							Operator: "Exists",
							Effect:   "NoSchedule",
						},
					},
					ServiceAccountName: common.OperatorServiceAccountName,
					InitContainers: []corev1.Container{
						{
							SecurityContext: &corev1.SecurityContext{
								Privileged: &priv,
								RunAsUser:  &runAs,
							},
							Name:    "aide",
							Image:   "docker.io/mrogers950/aide:latest",
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
						},
					},
					// make this an endless loop
					Containers: []corev1.Container{
						{
							Name:  "pause",
							Image: "gcr.io/google_containers/pause",
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
					},
				},
			},
		},
	}
}

func aideDaemonset() *appsv1.DaemonSet {
	priv := true
	runAs := int64(0)
	mode := int32(0744)

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.DaemonSetName,
			Namespace: common.FileIntegrityNamespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": common.DaemonSetName,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": common.DaemonSetName,
					},
				},
				Spec: corev1.PodSpec{
					Tolerations: []corev1.Toleration{
						{
							Key:      "node-role.kubernetes.io/master",
							Operator: "Exists",
							Effect:   "NoSchedule",
						},
					},
					ServiceAccountName: common.OperatorServiceAccountName,
					// The init container handles the reinitialization of the aide db after a configuration change
					InitContainers: []corev1.Container{
						{
							SecurityContext: &corev1.SecurityContext{
								Privileged: &priv,
								RunAsUser:  &runAs,
							},
							Name:    "aide-ds-init",
							Image:   "docker.io/mrogers950/aide:latest",
							Command: []string{common.AideScriptPath},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "hostroot",
									MountPath: "/hostroot",
								},
								{
									Name:      "config",
									MountPath: "/tmp",
								},
								{
									Name:      common.AideInitScriptConfigMapName,
									MountPath: "/scripts",
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							SecurityContext: &corev1.SecurityContext{
								Privileged: &priv,
								RunAsUser:  &runAs,
							},
							Name:    "aide",
							Image:   "docker.io/mrogers950/aide:latest",
							Command: []string{common.AideScriptPath},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "hostroot",
									MountPath: "/hostroot",
								},
								{
									Name:      "config",
									MountPath: "/tmp",
								},
								{
									Name:      common.AideScriptConfigMapName,
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
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "aide-conf",
									},
								},
							},
						},
						{
							Name: common.AideScriptConfigMapName,
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: common.AideScriptConfigMapName,
									},
									DefaultMode: &mode,
								},
							},
						},
						{
							Name: common.AideInitScriptConfigMapName,
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: common.AideInitScriptConfigMapName,
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
