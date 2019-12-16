package common

const (
	// AideConfigLabelKey tells us if a specific ConfigMap is an AIDE config
	AideConfigLabelKey = "file-integrity.openshift.io/aide-conf"
	// AideConfigUpdatedAnnotationKey tells us if an aide config needs updating
	AideConfigUpdatedAnnotationKey = "file-integrity.openshift.io/updated"
	AideInitScriptConfigMapName    = "aide-init"
	AideReinitScriptConfigMapName  = "aide-reinit"
	AideScriptConfigMapName        = "aide-script"
	AideScriptPath                 = "/scripts/aide.sh"
	DaemonSetName                  = "aide-ds"
	DefaultConfDataKey             = "aide.conf"
	DefaultConfigMapName           = "aide-conf"
	FileIntegrityNamespace         = "openshift-file-integrity"
	LogCollectorDaemonSetName      = "logcollector"
	OperatorServiceAccountName     = "file-integrity-operator"
	ReinitDaemonSetName            = "aide-reinit-ds"
)
