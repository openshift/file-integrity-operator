module github.com/openshift/file-integrity-operator

go 1.15

require (
	github.com/cenkalti/backoff/v4 v4.1.1
	github.com/coreos/ignition/v2 v2.9.0
	github.com/go-logr/logr v0.4.0
	github.com/go-logr/zapr v0.4.0 // indirect
	github.com/go-openapi/spec v0.19.4
	github.com/openshift/machine-config-operator v0.0.1-0.20200913004441-7eba765c69c9
	github.com/operator-framework/operator-registry v1.13.4
	github.com/operator-framework/operator-sdk v0.19.4
	github.com/pkg/errors v0.9.1
	github.com/securego/gosec/v2 v2.8.0
	github.com/spf13/cobra v1.1.1
	k8s.io/api v0.19.11
	k8s.io/apimachinery v0.19.11
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/kube-openapi v0.0.0-20200805222855-6aeccd4b50c6
	sigs.k8s.io/controller-runtime v0.6.2
)

// Pinned to kubernetes-1.16.2
replace (
	github.com/Azure/go-autorest => github.com/Azure/go-autorest v13.3.2+incompatible // Required by OLM
	github.com/openshift/machine-config-operator => github.com/openshift/machine-config-operator v0.0.1-0.20200913004441-7eba765c69c9
	k8s.io/api => k8s.io/api v0.19.11
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.19.11
	k8s.io/apimachinery => k8s.io/apimachinery v0.19.11
	k8s.io/apiserver => k8s.io/apiserver v0.19.11
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.19.11
	k8s.io/client-go => k8s.io/client-go v0.19.11
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.19.11
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.19.11
	k8s.io/code-generator => k8s.io/code-generator v0.19.11
	k8s.io/component-base => k8s.io/component-base v0.19.11
	k8s.io/cri-api => k8s.io/cri-api v0.19.11
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.19.11
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.19.11
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.19.11
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.19.11
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.19.11
	k8s.io/kubectl => k8s.io/kubectl v0.19.11
	k8s.io/kubelet => k8s.io/kubelet v0.19.11
	k8s.io/kubernetes => k8s.io/kubernetes v1.19.11
	k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.19.11
	k8s.io/metrics => k8s.io/metrics v0.19.11
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.19.11
)

replace github.com/docker/docker => github.com/moby/moby v0.7.3-0.20190826074503-38ab9da00309 // Required by Helm

replace github.com/openshift/api => github.com/openshift/api v0.0.0-20190924102528-32369d4db2ad // Required until https://github.com/operator-framework/operator-lifecycle-manager/pull/1241 is resolved

replace github.com/gorilla/websocket => github.com/gorilla/websocket v1.4.2
