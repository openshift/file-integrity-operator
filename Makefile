# Operator variables
# ==================
export APP_NAME=file-integrity-operator

# Container image variables
# =========================
IMAGE_REPO?=quay.io/file-integrity-operator
RUNTIME?=podman

# Image tag to use. Set this if you want to use a specific tag for building
# or your e2e tests.
TAG?=latest

# Image path to use. Set this if you want to use a specific path for building
# or your e2e tests. This is overwritten if we bulid the image and push it to
# the cluster or if we're on CI.
RELATED_IMAGE_OPERATOR_PATH?=$(IMAGE_REPO)/$(APP_NAME)
BUNDLE_IMAGE_PATH=$(IMAGE_REPO)/file-integrity-operator-bundle
BUNDLE_IMAGE_TAG?=$(TAG)
TEST_BUNDLE_IMAGE_TAG?=testonly
INDEX_IMAGE_NAME=file-integrity-operator-index
INDEX_IMAGE_PATH=$(IMAGE_REPO)/$(INDEX_IMAGE_NAME)
INDEX_IMAGE_TAG?=latest

#
DOWNLEVEL_VERSION?=0.1.16

# Build variables
# ===============
CURPATH=$(PWD)
TARGET_DIR=$(CURPATH)/build/_output
GO=GOFLAGS=-mod=vendor GO111MODULE=auto go
GOBUILD=$(GO) build
BUILD_GOPATH=$(TARGET_DIR):$(CURPATH)/cmd
TARGET_OPERATOR=$(TARGET_DIR)/bin/$(APP_NAME)
MAIN_PKG=cmd/manager/main.go
PKGS=$(shell go list ./... | grep -v -E '/vendor/|/test|/examples')

# go source files, ignore vendor directory
SRC = $(shell find . -type f -name '*.go' -not -path "./vendor/*" -not -path "./_output/*")


# Kubernetes variables
# ====================
KUBECONFIG?=$(HOME)/.kube/config
export NAMESPACE=openshift-file-integrity

# Operator-sdk variables
# ======================
SDK_VERSION?=v0.18.2
OPERATOR_SDK_URL=https://github.com/operator-framework/operator-sdk/releases/download/$(SDK_VERSION)/operator-sdk-$(SDK_VERSION)-x86_64-linux-gnu

OPM_VERSION=v1.15.2
OPM_URL=https://github.com/operator-framework/operator-registry/releases/download/$(OPM_VERSION)/linux-amd64-opm

# Test variables
# ==============
TEST_OPTIONS?=
# Skip pushing the container to your cluster
E2E_SKIP_CONTAINER_PUSH?=false
# Use default images in the e2e test run. Note that this takes precedence over E2E_SKIP_CONTAINER_PUSH
E2E_USE_DEFAULT_IMAGES?=false

# Pass extra flags to the e2e test run.
# e.g. to run a specific test in the e2e test suite, do:
# 	make e2e E2E_GO_TEST_FLAGS="-v -run TestE2E/TestScanWithNodeSelectorFiltersCorrectly"
E2E_GO_TEST_FLAGS?=-v -timeout 60m

# operator-sdk arguments for `make release`.
QUAY_NAMESPACE=file-integrity-operator
OPERATOR_VERSION?=
PACKAGE_CHANNEL?=alpha

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
image: fmt operator-sdk operator-image bundle-image  ## Build the file-integrity-operator container image

.PHONY: operator-image
operator-image: operator-sdk
	$(GOPATH)/bin/operator-sdk build $(RELATED_IMAGE_OPERATOR_PATH):$(TAG) --image-builder $(RUNTIME)

.PHONY: bundle-image
bundle-image:
	$(RUNTIME) build -t $(BUNDLE_IMAGE_PATH):$(BUNDLE_IMAGE_TAG) -f bundle.Dockerfile .

.PHONY: test-bundle-image
test-bundle-image:
	$(RUNTIME) build -t $(BUNDLE_IMAGE_PATH):$(TEST_BUNDLE_IMAGE_TAG) -f bundle.Dockerfile .

.PHONY: index-image
index-image: opm
	$(GOPATH)/bin/opm index add -b $(BUNDLE_IMAGE_PATH):$(BUNDLE_IMAGE_TAG) -f $(INDEX_IMAGE_PATH):$(INDEX_IMAGE_TAG) -t $(INDEX_IMAGE_PATH):$(INDEX_IMAGE_TAG) -c $(RUNTIME) --overwrite-latest

.PHONY: test-index-image
test-index-image: opm test-bundle-image push-test-bundle
	$(GOPATH)/bin/opm index add -b $(BUNDLE_IMAGE_PATH):$(TEST_BUNDLE_IMAGE_TAG) -t $(INDEX_IMAGE_PATH):$(INDEX_IMAGE_TAG) -c $(RUNTIME)

# Like the above, but for making an index image out of an old published (not built here) operator version. Useful for upgrade testing.
# Run `make index-downlevel` and then `make index-to-cluster` to push an index that installs the 0.1.16 bundle.
# Then run `make test-catalog` to build a test bundle and index from HEAD.
.PHONY: index-downlevel
index-downlevel: opm
	$(GOPATH)/bin/opm index add -b $(BUNDLE_IMAGE_PATH):$(DOWNLEVEL_VERSION) -t $(INDEX_IMAGE_PATH):$(INDEX_IMAGE_TAG) -c $(RUNTIME)

.PHONY: test-index-to-cluster
test-index-to-cluster: namespace openshift-user test-index-image index-to-cluster

# Doesn't build the index; for use after running `make index-downlevel`
.PHONY: index-to-cluster
index-to-cluster: namespace openshift-user
	@echo "Temporarily exposing the default route to the image registry"
	@oc patch configs.imageregistry.operator.openshift.io/cluster --patch '{"spec":{"defaultRoute":true}}' --type=merge
	@echo "Pushing image $(INDEX_IMAGE_PATH):$(INDEX_IMAGE_TAG) to the image registry"
	IMAGE_REGISTRY_HOST=$$(oc get route default-route -n openshift-image-registry --template='{{ .spec.host }}'); \
		$(RUNTIME) login --tls-verify=false -u $(OPENSHIFT_USER) -p $(shell oc whoami -t) $${IMAGE_REGISTRY_HOST}; \
		$(RUNTIME) push --tls-verify=false $(INDEX_IMAGE_PATH):$(INDEX_IMAGE_TAG) $${IMAGE_REGISTRY_HOST}/openshift/$(INDEX_IMAGE_NAME):$(INDEX_IMAGE_TAG)
	@echo "Removing the route from the image registry"
	@oc patch configs.imageregistry.operator.openshift.io/cluster --patch '{"spec":{"defaultRoute":false}}' --type=merge
	$(eval LOCAL_INDEX_IMAGE_PATH = image-registry.openshift-image-registry.svc:5000/openshift/$(INDEX_IMAGE_NAME):$(INDEX_IMAGE_TAG))

.PHONY: test-catalog
test-catalog: test-index-to-cluster
	@echo "WARNING: This will temporarily modify deploy/olm-catalog/catalog-source.yaml"
	@echo "Replacing image reference in deploy/olm-catalog/catalog-source.yaml to $(LOCAL_INDEX_IMAGE_PATH)"
	@sed -i 's%quay.io/file-integrity-operator/file-integrity-operator-index:latest%$(LOCAL_INDEX_IMAGE_PATH)%' deploy/olm-catalog/catalog-source.yaml
	@oc apply -f deploy/olm-catalog/catalog-source.yaml
	@echo "Restoring image reference in deploy/olm-catalog/catalog-source.yaml"
	@sed -i 's%$(LOCAL_INDEX_IMAGE_PATH)%quay.io/file-integrity-operator/file-integrity-operator-index:latest%' deploy/olm-catalog/catalog-source.yaml
	@oc apply -f deploy/olm-catalog/operator-group.yaml
	@oc apply -f deploy/olm-catalog/subscription.yaml

.PHONY: build
build: operator-bin ## Build the file-integrity-operator binaries

.PHONY: operator-bin
operator-bin:
	$(GO) build -o $(TARGET_OPERATOR) github.com/openshift/file-integrity-operator/cmd/manager

.PHONY: operator-sdk
operator-sdk: $(GOPATH)/bin/operator-sdk

$(GOPATH)/bin/operator-sdk:
	wget -nv $(OPERATOR_SDK_URL) -O $(GOPATH)/bin/operator-sdk || (echo "wget returned $$? trying to fetch operator-sdk. please install operator-sdk and try again"; exit 1)
	chmod +x $(GOPATH)/bin/operator-sdk

.PHONY: opm
opm: $(GOPATH)/bin/opm

$(GOPATH)/bin/opm:
	wget -nv $(OPM_URL) -O $(GOPATH)/bin/opm || (echo "wget returned $$? trying to fetch opm. please install opm and try again"; exit 1)
	chmod +x $(GOPATH)/bin/opm

.PHONY: run
run: operator-sdk ## Run the file-integrity-operator locally
	WATCH_NAMESPACE=$(NAMESPACE) \
	KUBERNETES_CONFIG=$(KUBECONFIG) \
	OPERATOR_NAME=$(APP_NAME) \
	$(GOPATH)/bin/operator-sdk run --local --watch-namespace $(NAMESPACE) --operator-flags operator

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
	@$(GO) run github.com/securego/gosec/v2/cmd/gosec -severity medium -confidence medium -quiet ./...

.PHONY: generate
generate: operator-sdk ## Run operator-sdk's code generation (k8s and crds)
	$(GOPATH)/bin/operator-sdk generate k8s
	$(GOPATH)/bin/operator-sdk generate crds

.PHONY: test-unit
test-unit: fmt ## Run the unit tests
	@$(GO) test $(TEST_OPTIONS) $(PKGS)

# Kept for backwards compatibility, the deploy-local target also exists in CO so
# it makes sense to deprecate deploy-to-cluster
deploy-to-cluster: deploy-local

# This runs the end-to-end tests. If not running this on CI, it'll try to
# push the operator image to the cluster's registry. This behavior can be
# avoided with the E2E_SKIP_CONTAINER_PUSH environment variable.
.PHONY: e2e
ifeq ($(E2E_SKIP_CONTAINER_PUSH), false)
e2e: namespace operator-sdk image-to-cluster openshift-user ## Run the end-to-end tests
else
e2e: namespace operator-sdk
endif
	@echo "Running e2e tests"
	@echo "WARNING: This will temporarily modify deploy/operator.yaml"
	@echo "Replacing workload references in deploy/operator.yaml"
	@sed -i 's%$(IMAGE_REPO)/$(APP_NAME):latest%$(RELATED_IMAGE_OPERATOR_PATH)%' deploy/operator.yaml
	unset GOFLAGS && $(GOPATH)/bin/operator-sdk test local ./tests/e2e --skip-cleanup-error --image "$(RELATED_IMAGE_OPERATOR_PATH)" --operator-namespace "$(NAMESPACE)" --go-test-flags "$(E2E_GO_TEST_FLAGS)"
	@echo "Restoring image references in deploy/operator.yaml"
	@sed -i 's%$(RELATED_IMAGE_OPERATOR_PATH)%$(IMAGE_REPO)/$(APP_NAME):latest%' deploy/operator.yaml

# If IMAGE_FROM_CI is not defined, it means that we're not running on CI, so we
# probably want to push the file-integrity-operator image to the cluster we're
# developing on. This target exposes temporarily the image registry, pushes the
# image, and remove the route in the end.
#
# The IMAGE_FROM_CI variable comes from CI. It is of the format:
#     <image path in CI registry>:${component}
# Here define the `component` variable, so, when we overwrite the
# RELATED_IMAGE_OPERATOR_PATH variable, it'll expand to the component we need.
#
# If the E2E_SKIP_CONTAINER_PUSH environment variable is used, the target will
# assume that you've pushed images beforehand, and will merely set the
# necessary variables to use them.
#
# If the E2E_USE_DEFAULT_IMAGES environment variable is used, this will do
# nothing, and the default images will be used.
.PHONY: image-to-cluster
ifdef IMAGE_FROM_CI
image-to-cluster:
	@echo "IMAGE_FROM_CI variable detected. We're in a CI enviornment."
	@echo "Skipping image-to-cluster target."
	$(eval component = $(APP_NAME))
	$(eval RELATED_IMAGE_OPERATOR_PATH = $(IMAGE_FROM_CI))
else ifeq ($(E2E_USE_DEFAULT_IMAGES), true)
image-to-cluster:
	@echo "E2E_USE_DEFAULT_IMAGES variable detected. Using default images."
else ifeq ($(E2E_SKIP_CONTAINER_PUSH), true)
image-to-cluster:
	@echo "E2E_SKIP_CONTAINER_PUSH variable detected. Using previously pushed images."
	$(eval RELATED_IMAGE_OPERATOR_PATH = image-registry.openshift-image-registry.svc:5000/$(NAMESPACE)/$(APP_NAME):$(TAG))
else
image-to-cluster: namespace openshift-user image
	@echo "IMAGE_FROM_CI variable missing. We're in local enviornment."
	@echo "Temporarily exposing the default route to the image registry"
	@oc patch configs.imageregistry.operator.openshift.io/cluster --patch '{"spec":{"defaultRoute":true}}' --type=merge
	@echo "Pushing image $(RELATED_IMAGE_OPERATOR_PATH):$(TAG) to the image registry"
	IMAGE_REGISTRY_HOST=$$(oc get route default-route -n openshift-image-registry --template='{{ .spec.host }}'); \
		$(RUNTIME) login --tls-verify=false -u $(OPENSHIFT_USER) -p $(shell oc whoami -t) $${IMAGE_REGISTRY_HOST}; \
		$(RUNTIME) push --tls-verify=false $(RELATED_IMAGE_OPERATOR_PATH):$(TAG) $${IMAGE_REGISTRY_HOST}/$(NAMESPACE)/$(APP_NAME):$(TAG)
	@echo "Removing the route from the image registry"
	@oc patch configs.imageregistry.operator.openshift.io/cluster --patch '{"spec":{"defaultRoute":false}}' --type=merge
	$(eval RELATED_IMAGE_OPERATOR_PATH = image-registry.openshift-image-registry.svc:5000/$(NAMESPACE)/$(APP_NAME):$(TAG))
endif

.PHONY: namespace
namespace:
	@echo "Creating '$(NAMESPACE)' namespace/project"
	@oc create -f deploy/ns.yaml || true

.PHONY: deploy
deploy: namespace deploy-crds ## Deploy the operator from the manifests in the deploy/ directory
	@oc apply -n $(NAMESPACE) -f deploy/

.PHONY: deploy-local
deploy-local: namespace image-to-cluster deploy-crds ## Deploy the operator from the manifests in the deploy/ directory and the images from a local build
	@sed -i 's%$(IMAGE_REPO)/$(APP_NAME):latest%$(RELATED_IMAGE_OPERATOR_PATH)%' deploy/operator.yaml
	@oc apply -n $(NAMESPACE) -f deploy/
	@oc apply -f deploy/olm-catalog/file-integrity-operator/manifests/monitoring_clusterrole.yaml
	@oc apply -f deploy/olm-catalog/file-integrity-operator/manifests/monitoring_clusterrolebinding.yaml
	@sed -i 's%$(RELATED_IMAGE_OPERATOR_PATH)%$(IMAGE_REPO)/$(APP_NAME):latest%' deploy/operator.yaml

.PHONY: deploy-crds
deploy-crds:
	@for crd in $(shell ls -1 deploy/crds/*crd.yaml) ; do \
		oc apply -f $$crd ; \
	done

.PHONY: tear-down
tear-down: tear-down-crds tear-down-operator ## Tears down all objects required for the operator except the namespace


.PHONY: tear-down-crds
tear-down-crds:
	@for crd in $(shell ls -1 deploy/crds/*crd.yaml) ; do \
		oc delete --ignore-not-found -f $$crd ; \
	done

.PHONY: tear-down-operator
tear-down-operator:
	@for manifest in $(shell ls -1 deploy/*.yaml | grep -v ns.yaml) ; do \
		oc delete --ignore-not-found -f $$manifest ; \
	done

.PHONY: openshift-user
openshift-user:
ifeq ($(shell oc whoami 2>/dev/null),kube:admin)
	$(eval OPENSHIFT_USER = kubeadmin)
else
	$(eval OPENSHIFT_USER = $(shell oc whoami))
endif

.PHONY: push
push: image push-bundle
	$(RUNTIME) push $(RELATED_IMAGE_OPERATOR_PATH):$(TAG)

.PHONY: push-bundle
push-bundle: bundle-image
	# bundle image
	$(RUNTIME) push $(BUNDLE_IMAGE_PATH):$(BUNDLE_IMAGE_TAG)

.PHONY: push-test-bundle
push-test-bundle: test-bundle-image
	# test bundle image
	$(RUNTIME) push $(BUNDLE_IMAGE_PATH):$(TEST_BUNDLE_IMAGE_TAG)

.PHONY: push-index
push-index: index-image
	$(RUNTIME) push $(INDEX_IMAGE_PATH):latest

.PHONY: check-operator-version
check-operator-version:
ifndef OPERATOR_VERSION
	$(error OPERATOR_VERSION must be defined)
endif

.PHONY: bundle
bundle: check-operator-version operator-sdk ## Generate the bundle and packaging for the specific version (NOTE: Gotta specify the version with the OPERATOR_VERSION environment variable)
	$(GOPATH)/bin/operator-sdk generate bundle -q --overwrite --version "$(OPERATOR_VERSION)"
	sed -i '/replaces:/d' deploy/olm-catalog/file-integrity-operator/manifests/file-integrity-operator.clusterserviceversion.yaml
	sed -i "s/\(olm.skipRange: '>=.*\)<.*'/\1<$(OPERATOR_VERSION)'/" deploy/olm-catalog/file-integrity-operator/manifests/file-integrity-operator.clusterserviceversion.yaml
	$(GOPATH)/bin/operator-sdk bundle validate ./deploy/olm-catalog/file-integrity-operator/

.PHONY: package-version-to-tag
package-version-to-tag: check-operator-version
	@echo "Overriding default tag '$(TAG)' with release tag '$(OPERATOR_VERSION)'"
	$(eval TAG = $(OPERATOR_VERSION))

.PHONY: release-tag-image
release-tag-image: package-version-to-tag
	@echo "Temporarily overriding image tags in deploy/operator.yaml"
	@sed -i 's%$(IMAGE_REPO)/$(APP_NAME):latest%$(RELATED_IMAGE_OPERATOR_PATH):$(TAG)%' deploy/operator.yaml

.PHONY: undo-deploy-tag-image
undo-deploy-tag-image: package-version-to-tag
	@echo "Restoring image tags in deploy/operator.yaml"
	@sed -i 's%$(RELATED_IMAGE_OPERATOR_PATH):$(TAG)%$(IMAGE_REPO)/$(APP_NAME):latest%' deploy/operator.yaml

.PHONY: git-release
git-release: package-version-to-tag changelog
	git checkout -b "release-v$(TAG)"
	git add "deploy/olm-catalog/file-integrity-operator/"
	git add "CHANGELOG.md"
	git commit -m "Release v$(TAG)"
	git tag "v$(TAG)"
	git push origin "v$(TAG)"
	git push origin "release-v$(TAG)"

.PHONY: release
release: release-tag-image bundle push push-index undo-deploy-tag-image git-release ## Do an official release (Requires permissions)
	# This will ensure that we also push to the latest tag
	$(MAKE) push TAG=latest

.PHONY: changelog
changelog:
	@utils/update_changelog.sh "$(TAG)"
