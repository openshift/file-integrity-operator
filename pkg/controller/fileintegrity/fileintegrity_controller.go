package fileintegrity

import (
	"context"
	"fmt"
	"strconv"
	"strings"

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
		Name:      common.PauseConfigMapName,
		Namespace: common.FileIntegrityNamespace,
	}, cm); err != nil {
		if !kerr.IsNotFound(err) {
			return nil, err
		}
		// does not exist, create
		if err := r.client.Create(context.TODO(), aidePauseScript()); err != nil {
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

// reconcileUserConfig checks if the user provided a configuration of their own and prepares it. Returns true if a new
// configuration was added, false if not.
func (r *ReconcileFileIntegrity) reconcileUserConfig(instance *fileintegrityv1alpha1.FileIntegrity,
	reqLogger logr.Logger, currentConfig *corev1.ConfigMap) (bool, error) {
	if len(instance.Spec.Config.Name) == 0 || len(instance.Spec.Config.Namespace) == 0 {
		return false, nil
	}

	reqLogger.Info("reconciling user-provided configMap")

	userConfigMap := &corev1.ConfigMap{}
	err := r.client.Get(context.TODO(), types.NamespacedName{
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
			reqLogger.Info("Re-init forced, creating daemonSet.")
		} else {
			reqLogger.Info("Re-init triggered by configuration change, creating daemonSet.")
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
		reqLogger.Info("Removing re-init annotation.")
		if err := r.client.Update(context.TODO(), fiCopy); err != nil {
			return reconcile.Result{}, err
		}
	}

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
	} else {
		// Handle an update of the daemon arguments when Debug and GracePeriod are changed. If they constitute a change
		// then the daemonSet spec is updated and the pods are restarted.
		updated, newArgs, err := updateDaemonSetPodArgs(daemonSet, instance)
		if err != nil {
			return reconcile.Result{}, nil
		}
		if updated {
			dsCopy := daemonSet.DeepCopy()
			dsCopy.Spec.Template.Spec.Containers[0].Args = newArgs
			if err := r.client.Update(context.TODO(), dsCopy); err != nil {
				return reconcile.Result{}, err
			}

			err := common.RestartFileIntegrityDs(r.client, common.GetDaemonSetName(instance.Name))
			if err != nil {
				return reconcile.Result{}, err
			}
			reqLogger.Info("FileIntegrity daemon configuration changed - pods restarted.")
		}
	}
	return reconcile.Result{}, nil
}

func intervalOpt(s string) bool {
	return strings.HasPrefix(s, "--interval=")
}

func debugOpt(s string) bool {
	return strings.HasPrefix(s, "--debug=")
}

// Returns true with the daemon pod args from fi (for --interval and --debug options) if they differ from the current DS.
// Returns false if there was no difference.
func updateDaemonSetPodArgs(currentDS *appsv1.DaemonSet, fi *fileintegrityv1alpha1.FileIntegrity) (bool, []string, error) {
	var currentGracePeriod string
	var currentDebug string

	args := currentDS.Spec.Template.Spec.Containers[0].Args
	newArgs := make([]string, 0)

	for _, arg := range args {
		if intervalOpt(arg) {
			currentGracePeriod = arg[len("--interval="):]
		} else if debugOpt(arg) {
			currentDebug = arg[len("--debug="):]
		}
	}
	if currentGracePeriod == "" || currentDebug == "" {
		return false, newArgs, fmt.Errorf("bad daemon configuration")
	}

	newGracePeriod := getGracePeriod(fi)
	newDebug := getDebug(fi)
	if currentGracePeriod != newGracePeriod || currentDebug != newDebug {
		for _, arg := range args {
			if !intervalOpt(arg) && !debugOpt(arg) {
				newArgs = append(newArgs, arg)
			}
		}
		newArgs = append(newArgs, fmt.Sprintf("--interval=%s", newGracePeriod))
		newArgs = append(newArgs, fmt.Sprintf("--debug=%s", newDebug))
		return true, newArgs, nil
	}
	return false, newArgs, nil
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
			"pause.sh": aidePauseContainerScript,
		},
	}
}

// reinitAideDaemonset returns a DaemonSet that runs a one-shot pod on each node. This pod touches a file
// on the host OS that informs the AIDE daemon to back up and reinitialize the AIDE db.
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
							Name:    "pause",
							Command: []string{common.PausePath},
							Image:   common.GetComponentImage(common.AIDE),
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      common.PauseConfigMapName,
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

func aideDaemonset(dsName string, fi *fileintegrityv1alpha1.FileIntegrity) *appsv1.DaemonSet {
	priv := true
	runAs := int64(0)
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
					Containers: []corev1.Container{
						{
							SecurityContext: &corev1.SecurityContext{
								Privileged: &priv,
								RunAsUser:  &runAs,
							},
							Name:  "daemon",
							Image: common.GetComponentImage(common.OPERATOR),
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
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "hostroot",
									MountPath: "/hostroot",
								},
								{
									Name:      "config",
									MountPath: "/tmp",
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
					},
				},
			},
		},
	}
}

func getGracePeriod(fi *fileintegrityv1alpha1.FileIntegrity) string {
	gracePeriod := fi.Spec.Config.GracePeriod
	if gracePeriod < 10 {
		gracePeriod = 10
	}
	return strconv.Itoa(gracePeriod)
}

func getDebug(fi *fileintegrityv1alpha1.FileIntegrity) string {
	return strconv.FormatBool(fi.Spec.Debug)
}

func daemonArgs(dsName string, fi *fileintegrityv1alpha1.FileIntegrity) []string {
	return []string{"daemon",
		"--lc-file=" + aideLogPath,
		"--lc-config-map-prefix=" + dsName,
		"--lc-owner=" + fi.Name,
		"--lc-namespace=" + fi.Namespace,
		"--interval=" + getGracePeriod(fi),
		"--debug=" + getDebug(fi),
	}
}
