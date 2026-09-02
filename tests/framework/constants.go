package framework

import "time"

const (
	RetryInterval    = time.Second * 5
	Timeout          = time.Minute * 30
	RhcosContentFile = "ssg-rhcos4-ds.xml"
	OperatorName     = "file-integrity-operator"
)
