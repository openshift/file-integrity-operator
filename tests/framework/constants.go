package framework

import "time"

const (
	RetryInterval = time.Second * 5
	Timeout       = time.Minute * 30
)
