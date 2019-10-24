package common

const (
	FileIntegrityNamespace     = "openshift-file-integrity"
	DefaultConfDataKey         = "aide.conf"
	DefaultConfigMapName       = "aide-conf"
	WorkerDaemonSetName        = "aide-worker"
	MasterDaemonSetName        = "aide-master"
	AideScriptConfigMapName    = "aide-script"
	OperatorServiceAccountName = "file-integrity-operator"
	AideScriptPath             = "/scripts/aide.sh"
)
