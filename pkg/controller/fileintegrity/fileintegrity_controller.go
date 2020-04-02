package fileintegrity

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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

var log = logf.Log.WithName("controller_fileintegrity")

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
func (r *ReconcileFileIntegrity) handleDefaultConfigMaps(f *fileintegrityv1alpha1.FileIntegrity) (*corev1.ConfigMap, error) {
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
		Name:      f.Name,
		Namespace: common.FileIntegrityNamespace,
	}, cm); err != nil {
		if !kerr.IsNotFound(err) {
			return nil, err
		}
		// does not exist, create
		if err := r.client.Create(context.TODO(), defaultAIDEConfigMap(f.Name)); err != nil {
			return nil, err
		}
	} else if _, ok := cm.Data[common.DefaultConfDataKey]; !ok {
		// we had the configMap but its data was missing for some reason, so restore it.
		if err := r.client.Update(context.TODO(), defaultAIDEConfigMap(f.Name)); err != nil {
			return nil, err
		}
	}

	return cm, nil
}

func (r *ReconcileFileIntegrity) createReinitDaemonSet(instance *fileintegrityv1alpha1.FileIntegrity) error {
	daemonSet := &appsv1.DaemonSet{}
	dsName := common.GetReinitDaemonSetName(instance.Name)
	dsNamespace := common.FileIntegrityNamespace

	err := r.client.Get(context.TODO(), types.NamespacedName{Name: dsName, Namespace: dsNamespace}, daemonSet)
	if err == nil {
		// Exists, so continue.
		return nil
	}

	if !kerr.IsNotFound(err) {
		return err
	}

	ds := reinitAideDaemonset(common.GetReinitDaemonSetName(instance.Name), instance)
	if err := controllerutil.SetControllerReference(instance, ds, r.scheme); err != nil {
		return err
	}

	return r.client.Create(context.TODO(), ds)
}

func (r *ReconcileFileIntegrity) updateAideConfig(conf *corev1.ConfigMap, data string) error {
	confCopy := conf.DeepCopy()
	confCopy.Data[common.DefaultConfDataKey] = data

	if confCopy.Annotations == nil {
		confCopy.Annotations = map[string]string{}
	}

	// Mark the configMap as updated by the user-provided config, for the configMap-controller to trigger an update.
	confCopy.Annotations[common.AideConfigUpdatedAnnotationKey] = "true"

	return r.client.Update(context.TODO(), confCopy)
}

func (r *ReconcileFileIntegrity) retrieveAndAnnotateAideConfig(conf *corev1.ConfigMap) error {
	cachedconf := &corev1.ConfigMap{}
	// Get the latest config...
	r.client.Get(context.TODO(), types.NamespacedName{Name: conf.Name, Namespace: conf.Namespace}, cachedconf)

	return r.updateAideConfig(cachedconf, cachedconf.Data[common.DefaultConfDataKey])
}

// reconcileUserConfig checks if the user provided a configuration of their own and preapres it
// returns true if new configuration was added and false if not.
func (r *ReconcileFileIntegrity) reconcileUserConfig(instance *fileintegrityv1alpha1.FileIntegrity, reqLogger logr.Logger, currentConfig *corev1.ConfigMap) (bool, error) {
	if len(instance.Spec.Config.Name) == 0 || len(instance.Spec.Config.Namespace) == 0 {
		return false, nil
	}

	reqLogger.Info("reconciling user-provided configMap")

	userConfigMap := &corev1.ConfigMap{}
	err := r.client.Get(context.TODO(), types.NamespacedName{Name: instance.Spec.Config.Name, Namespace: instance.Spec.Config.Namespace}, userConfigMap)
	if err != nil {
		if !kerr.IsNotFound(err) {
			reqLogger.Error(err, "error getting aide config configMap")
			return false, err
		}
		// FIXME(jaosorior): This should probably be an error instead
		reqLogger.Info(fmt.Sprintf("warning: user-specified configMap %s/%s does not exist", instance.Spec.Config.Namespace, instance.Spec.Config.Name))
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

	if err := r.updateAideConfig(currentConfig, preparedConf); err != nil {
		return false, err
	}

	return true, nil
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

	daemonSetName := common.GetDaemonSetName(instance.Name)

	defaultAideConf, err := r.handleDefaultConfigMaps(instance)
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

	_, forceReinit := instance.Annotations[common.AideDatabaseReinitAnnotationKey]
	if hasNewConfig || forceReinit {
		if forceReinit {
			reqLogger.Info("Re-init annotation found. Spawning reinit DS.")
		}
		// This daemonset re-inits the database
		// TODO(jaosorior): Add status about the re-init happening.
		if err := r.createReinitDaemonSet(instance); err != nil {
			return reconcile.Result{}, err
		}
	}

	// Remove re-init annotation
	if forceReinit {
		reqLogger.Info("Annotating AIDE config to be updated.")
		if err := r.retrieveAndAnnotateAideConfig(defaultAideConf); err != nil {
			return reconcile.Result{}, err
		}
		fiCopy := instance.DeepCopy()
		delete(fiCopy.Annotations, common.AideDatabaseReinitAnnotationKey)
		reqLogger.Info("Removing re-init DS.")
		if err := r.client.Update(context.TODO(), fiCopy); err != nil {
			return reconcile.Result{}, err
		}
	}

	reqLogger.Info("reconciling daemonSets")
	daemonSet := &appsv1.DaemonSet{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: daemonSetName, Namespace: common.FileIntegrityNamespace}, daemonSet)
	if err != nil {
		if !kerr.IsNotFound(err) {
			reqLogger.Error(err, "error getting daemonSet")
			return reconcile.Result{}, err
		}
		// create
		ds := aideDaemonset(daemonSetName, instance)

		if ownerErr := controllerutil.SetControllerReference(instance, ds, r.scheme); ownerErr != nil {
			log.Error(ownerErr, "Failed to set daemonset ownership", "DaemonSet", ds)
			return reconcile.Result{}, err
		}
		if createErr := r.client.Create(context.TODO(), ds); createErr != nil {
			reqLogger.Error(createErr, "error creating daemonSet")
			return reconcile.Result{}, common.IgnoreAlreadyExists(createErr)
		}
	}
	return reconcile.Result{}, nil
}

func defaultAIDEConfigMap(name string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: common.FileIntegrityNamespace,
			Labels: map[string]string{
				common.AideConfigLabelKey: "",
			},
		},
		Data: map[string]string{
			"aide.conf": DefaultAideConfig,
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
func reinitAideDaemonset(reinitDaemonSetName string, fi *fileintegrityv1alpha1.FileIntegrity) *appsv1.DaemonSet {
	priv := true
	runAs := int64(0)
	mode := int32(0744)

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      reinitDaemonSetName,
			Namespace: common.FileIntegrityNamespace,
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
					NodeSelector: fi.Spec.NodeSelector,
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
							Image:   common.GetComponentImage(common.AIDE),
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

func aideDaemonset(dsName string, fi *fileintegrityv1alpha1.FileIntegrity) *appsv1.DaemonSet {
	priv := true
	runAs := int64(0)
	mode := int32(0744)

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dsName,
			Namespace: common.FileIntegrityNamespace,
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
						"app": dsName,
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: fi.Spec.NodeSelector,
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
							Image:   common.GetComponentImage(common.AIDE),
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
							Image:   common.GetComponentImage(common.AIDE),
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
						{
							SecurityContext: &corev1.SecurityContext{
								Privileged: &priv,
							},
							Name:  "logcollector",
							Image: common.GetComponentImage(common.LOGCOLLECTOR),
							Args: []string{
								"--file=" + aideLogPath,
								"--config-map-prefix=" + dsName,
								"--owner=" + fi.Name,
								"--namespace=" + fi.Namespace,
								"--timeout=2",
								"--interval=10",
								// TODO: remove this for production
								"--debug=true",
							},
							Env: []corev1.EnvVar{
								{
									Name: "NODE_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "spec.nodeName",
										},
									},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "hostroot",
									MountPath: "/hostroot",
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
										Name: fi.Name,
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
