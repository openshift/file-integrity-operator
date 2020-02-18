# Operator variables
# ==================
export APP_NAME=file-integrity-operator
LOG_COLLECTOR=file-integrity-logcollector
AIDE=file-integrity-aide

# Container image variables
# =========================
IMAGE_REPO?=docker.io/mrogers950
RUNTIME?=podman

# Image path to use. Set this if you want to use a specific path for building
# or your e2e tests. This is overwritten if we bulid the image and push it to
# the cluster or if we're on CI.
OPERATOR_IMAGE_PATH?=$(IMAGE_REPO)/$(APP_NAME)
LOGCOLLECTOR_IMAGE_PATH?=$(IMAGE_REPO)/$(LOG_COLLECTOR)
LOGCOLLECTOR_DOCKERFILE_PATH?=./images/logcollector/Dockerfile
AIDE_IMAGE_PATH?=$(IMAGE_REPO)/$(AIDE)
AIDE_DOCKERFILE_PATH?=./images/aide/Dockerfile

# Image tag to use. Set this if you want to use a specific tag for building
# or your e2e tests.
TAG?=latest

# Build variables
# ===============
CURPATH=$(PWD)
TARGET_DIR=$(CURPATH)/build/_output
GO=GOFLAGS=-mod=vendor GO111MODULE=auto go
GOBUILD=$(GO) build
BUILD_GOPATH=$(TARGET_DIR):$(CURPATH)/cmd
TARGET_OPERATOR=$(TARGET_DIR)/bin/$(APP_NAME)
TARGET_LOGCOLLECTOR=$(TARGET_DIR)/bin/$(LOG_COLLECTOR)
MAIN_PKG=cmd/manager/main.go
PKGS=$(shell go list ./... | grep -v -E '/vendor/|/test|/examples')

# go source files, ignore vendor directory
SRC = $(shell find . -type f -name '*.go' -not -path "./vendor/*" -not -path "./_output/*")


# Kubernetes variables
# ====================
KUBECONFIG?=$(HOME)/.kube/config
export NAMESPACE?=openshift-file-integrity

# Operator-sdk variables
# ======================
SDK_VERSION?=v0.13.0
OPERATOR_SDK_URL=https://github.com/operator-framework/operator-sdk/releases/download/$(SDK_VERSION)/operator-sdk-$(SDK_VERSION)-x86_64-linux-gnu

# Test variables
# ==============
TEST_OPTIONS?=
# Skip pushing the container to your cluster
E2E_SKIP_CONTAINER_PUSH?=false

# Pass extra flags to the e2e test run.
# e.g. to run a specific test in the e2e test suite, do:
# 	make e2e E2E_GO_TEST_FLAGS="-v -run TestE2E/TestScanWithNodeSelectorFiltersCorrectly"
E2E_GO_TEST_FLAGS?=-v -timeout 30m

# operator-courier arguments for `make publish`.
# Before running `make publish`, install operator-courier with `pip3 install operator-courier` and create
# ~/.quay containing your quay.io token.
COURIER_CMD=operator-courier
COURIER_PACKAGE_NAME=file-integrity-operator-bundle
COURIER_OPERATOR_DIR=deploy/olm-catalog/file-integrity-operator
COURIER_QUAY_NAMESPACE=file-integrity-operator
COURIER_PACKAGE_VERSION="0.1.0"
COURIER_QUAY_TOKEN?= $(shell cat ~/.quay)

.PHONY: all
all: build ## Test and Build the file-integrity-operator

.PHONY: help
help: ## Show this help screen
	@echo 'Usage: make <OPTIONS> ... <TARGETS>'
	@echo ''
	@echo 'Available targets are:'
	@echo ''
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)


.PHONY: image
image: operator-image logcollector-image aide-image fmt operator-sdk ## Build the file-integrity-operator container image

operator-image:
	$(GOPATH)/bin/operator-sdk build $(OPERATOR_IMAGE_PATH) --image-builder $(RUNTIME)

logcollector-image:
	$(RUNTIME) build -f $(LOGCOLLECTOR_DOCKERFILE_PATH) -t $(LOGCOLLECTOR_IMAGE_PATH):$(TAG) .

aide-image:
	$(RUNTIME) build -f $(AIDE_DOCKERFILE_PATH) -t $(AIDE_IMAGE_PATH):$(TAG) .

.PHONY: build
build: operator-bin logcollector-bin ## Build the file-integrity-operator binaries

operator-bin:
	$(GO) build -o $(TARGET_OPERATOR) github.com/openshift/file-integrity-operator/cmd/manager

logcollector-bin:
	$(GO) build -o $(TARGET_LOGCOLLECTOR) github.com/openshift/file-integrity-operator/cmd/logcollector

.PHONY: operator-sdk
operator-sdk:
ifeq ("$(wildcard $(GOPATH)/bin/operator-sdk)","")
	wget -nv $(OPERATOR_SDK_URL) -O $(GOPATH)/bin/operator-sdk || (echo "wget returned $$? trying to fetch operator-sdk. please install operator-sdk and try again"; exit 1)
	chmod +x $(GOPATH)/bin/operator-sdk
endif

.PHONY: run
run: operator-sdk ## Run the file-integrity-operator locally
	WATCH_NAMESPACE=$(NAMESPACE) \
	KUBERNETES_CONFIG=$(KUBECONFIG) \
	OPERATOR_NAME=$(APP_NAME) \
	$(GOPATH)/bin/operator-sdk up local --namespace $(NAMESPACE)

.PHONY: clean
clean: clean-modcache clean-cache clean-output ## Clean the golang environment

.PHONY: clean-output
clean-output:
	rm -rf $(TARGET_DIR)

.PHONY: clean-cache
clean-cache:
	$(GO) clean -cache -testcache $(PKGS)

.PHONY: clean-modcache
clean-modcache:
	$(GO) clean -modcache $(PKGS)

.PHONY: fmt
fmt:  ## Run the `go fmt` tool
	@$(GO) fmt $(PKGS)

.PHONY: simplify
simplify:
	@gofmt -s -l -w $(SRC)

.PHONY: verify
verify: vet mod-verify gosec ## Run code lint checks

.PHONY: vet
vet:
	@$(GO) vet $(PKGS)

.PHONY: mod-verify
mod-verify:
	@$(GO) mod verify

.PHONY: gosec
gosec:
	@$(GO) run github.com/securego/gosec/cmd/gosec -severity medium -confidence medium -quiet ./...

.PHONY: generate
generate: operator-sdk ## Run operator-sdk's code generation (k8s and openapi)
	$(GOPATH)/bin/operator-sdk generate k8s
	$(GOPATH)/bin/operator-sdk generate openapi

.PHONY: test-unit
test-unit: fmt ## Run the unit tests
	@$(GO) test $(TEST_OPTIONS) $(PKGS)

# This runs the end-to-end tests. If not running this on CI, it'll try to
# push the operator image to the cluster's registry. This behavior can be
# avoided with the E2E_SKIP_CONTAINER_PUSH environment variable.
.PHONY: e2e
ifeq ($(E2E_SKIP_CONTAINER_PUSH), false)
e2e: namespace operator-sdk check-if-ci image-to-cluster ## Run the end-to-end tests
else
e2e: namespace operator-sdk check-if-ci
endif
	@echo "Running e2e tests"
	unset GOFLAGS && $(GOPATH)/bin/operator-sdk test local ./tests/e2e --image "$(OPERATOR_IMAGE_PATH)" --namespace "$(NAMESPACE)" --go-test-flags "$(E2E_GO_TEST_FLAGS)"

# This checks if we're in a CI environment by checking the IMAGE_FORMAT
# environmnet variable. if we are, lets ues the image from CI and use this
# operator as the component.
#
# The IMAGE_FORMAT variable comes from CI. It is of the format:
#     <image path in CI registry>:${component}
# Here define the `component` variable, so, when we overwrite the
# OPERATOR_IMAGE_PATH variable, it'll expand to the component we need.
.PHONY: check-if-ci
check-if-ci:
ifdef IMAGE_FORMAT
	@echo "IMAGE_FORMAT variable detected. We're in a CI enviornment."
	$(eval component = $(APP_NAME))
	$(eval OPERATOR_IMAGE_PATH = $(IMAGE_FORMAT))
else
	@echo "IMAGE_FORMAT variable missing. We're in local enviornment."
endif

# If IMAGE_FORMAT is not defined, it means that we're not running on CI, so we
# probably want to push the file-integrity-operator image to the cluster we're
# developing on. This target exposes temporarily the image registry, pushes the
# image, and remove the route in the end.
.PHONY: image-to-cluster
ifdef IMAGE_FORMAT
image-to-cluster:
	@echo "We're in a CI environment, skipping image-to-cluster target."
else
image-to-cluster: namespace openshift-user image
	@echo "Temporarily exposing the default route to the image registry"
	@oc patch configs.imageregistry.operator.openshift.io/cluster --patch '{"spec":{"defaultRoute":true}}' --type=merge
	@echo "Pushing image $(OPERATOR_IMAGE_PATH):$(TAG) to the image registry"
	IMAGE_REGISTRY_HOST=$$(oc get route default-route -n openshift-image-registry --template='{{ .spec.host }}'); \
		$(RUNTIME) login --tls-verify=false -u $(OPENSHIFT_USER) -p $(shell oc whoami -t) $${IMAGE_REGISTRY_HOST}; \
		$(RUNTIME) push --tls-verify=false $(OPERATOR_IMAGE_PATH):$(TAG) $${IMAGE_REGISTRY_HOST}/$(NAMESPACE)/$(APP_NAME):$(TAG); \
		$(RUNTIME) push --tls-verify=false $(LOGCOLLECTOR_IMAGE_PATH):$(TAG) $${IMAGE_REGISTRY_HOST}/$(NAMESPACE)/$(LOG_COLLECTOR):$(TAG); \
		$(RUNTIME) push --tls-verify=false $(AIDE_IMAGE_PATH):$(TAG) $${IMAGE_REGISTRY_HOST}/$(NAMESPACE)/$(AIDE):$(TAG)
	@echo "Removing the route from the image registry"
	@oc patch configs.imageregistry.operator.openshift.io/cluster --patch '{"spec":{"defaultRoute":false}}' --type=merge
	$(eval OPERATOR_IMAGE_PATH = image-registry.openshift-image-registry.svc:5000/$(NAMESPACE)/$(APP_NAME):$(TAG))
endif

.PHONY: namespace
namespace:
	@echo "Creating '$(NAMESPACE)' namespace/project"
	@oc create -f deploy/ns.yaml || true

.PHONY: openshift-user
openshift-user:
ifeq ($(shell oc whoami 2>/dev/null),kube:admin)
	$(eval OPENSHIFT_USER = kubeadmin)
else
	$(eval OPENSHIFT_USER = $(oc whoami))
endif

.PHONY: push
push: image
	$(RUNTIME) tag $(OPERATOR_IMAGE_PATH) $(OPERATOR_IMAGE_PATH):$(TAG)
	$(RUNTIME) push $(OPERATOR_IMAGE_PATH):$(TAG)

.PHONY: publish
publish:
	$(COURIER_CMD) push "$(COURIER_OPERATOR_DIR)" "$(COURIER_QUAY_NAMESPACE)" "$(COURIER_PACKAGE_NAME)" "$(COURIER_PACKAGE_VERSION)" "basic $(COURIER_QUAY_TOKEN)"
