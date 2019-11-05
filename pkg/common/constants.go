package common

const (
	FileIntegrityNamespace        = "openshift-file-integrity"
	DefaultConfDataKey            = "aide.conf"
	DefaultConfigMapName          = "aide-conf"
	WorkerDaemonSetName           = "aide-worker"
	MasterDaemonSetName           = "aide-master"
	WorkerReinitDaemonSetName     = "aide-reinit-worker"
	MasterReinitDaemonSetName     = "aide-reinit-master"
	AideScriptConfigMapName       = "aide-script"
	AideInitScriptConfigMapName   = "aide-init"
	AideReinitScriptConfigMapName = "aide-reinit"
	OperatorServiceAccountName    = "file-integrity-operator"
	AideScriptPath                = "/scripts/aide.sh"
)
