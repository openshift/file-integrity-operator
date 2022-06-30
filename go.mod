module github.com/openshift/file-integrity-operator

go 1.16

require (
	github.com/cenkalti/backoff/v4 v4.1.2
	github.com/coreos/ignition/v2 v2.13.0
	github.com/go-logr/logr v0.4.0
	github.com/mitchellh/go-homedir v1.1.0
	github.com/onsi/ginkgo v1.16.5 // indirect
	github.com/openshift/library-go v0.0.0-20200831114015-2ab0c61c15de
	github.com/openshift/machine-config-operator v0.0.1-0.20200913004441-7eba765c69c9
	github.com/operator-framework/operator-registry v1.21.0
	github.com/pborman/uuid v1.2.0
	github.com/pkg/errors v0.9.1
	github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring v0.52.1
	github.com/prometheus-operator/prometheus-operator/pkg/client v0.52.1
	github.com/prometheus/client_golang v1.11.0
	github.com/prometheus/client_model v0.2.0
	github.com/securego/gosec/v2 v2.11.0
	github.com/sirupsen/logrus v1.8.1
	github.com/spf13/cobra v1.2.1
	github.com/stretchr/testify v1.7.0
	go.uber.org/zap v1.19.1 // indirect
	golang.org/x/mod v0.5.1
	golang.org/x/net v0.0.0-20211112202133-69e39bad7dc2
	k8s.io/api v0.22.3
	k8s.io/apiextensions-apiserver v0.22.3
	k8s.io/apimachinery v0.22.3
	k8s.io/client-go v0.22.3
	rsc.io/letsencrypt v0.0.3 // indirect
	sigs.k8s.io/controller-runtime v0.10.0
	sigs.k8s.io/controller-tools v0.7.0
	sigs.k8s.io/yaml v1.2.0
)
