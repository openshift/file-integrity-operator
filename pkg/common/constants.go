package common

const (
	FileIntegrityNamespace        = "openshift-file-integrity"
	DefaultConfDataKey            = "aide.conf"
	DefaultConfigMapName          = "aide-conf"
	DaemonSetName                 = "aide-ds"
	ReinitDaemonSetName           = "aide-reinit-ds"
	AideScriptConfigMapName       = "aide-script"
	AideInitScriptConfigMapName   = "aide-init"
	AideReinitScriptConfigMapName = "aide-reinit"
	OperatorServiceAccountName    = "file-integrity-operator"
	AideScriptPath                = "/scripts/aide.sh"
)
