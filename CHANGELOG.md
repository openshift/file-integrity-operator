# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic
Versioning](https://semver.org/spec/v2.0.0.html).

## [1.2.0] - 2023-03-31
### Changes
- Use RHEL8 for building FIO container images in CI
- Bump github.com/go-logr/logr from 1.2.3 to 1.2.4
- Bump sigs.k8s.io/controller-runtime from 0.14.5 to 0.14.6
- Bump github.com/prometheus-operator/prometheus-operator/pkg/client
- Bump k8s.io/apiextensions-apiserver from 0.26.2 to 0.26.3
- Update CI to use RHEL 9 and OpenShift 4.13
- Bump github.com/onsi/gomega from 1.27.2 to 1.27.4
- Update maintainers
- Release v1.2.0
- Bump golang.org/x/net from 0.7.0 to 0.8.0
- Bump golang.org/x/mod from 0.8.0 to 0.9.0
- Use remote detection in release process
- dependabot.yaml: Remove duplicate value
- Update golang version in Dockerfile for builds
- Bump operator-sdk from 1.15.0 to 1.27.0
- chore: go fmt fixes
- Bump golang dependency to 1.19
- Configure dependabot
- Update coreos/ignition/v2 to 2.14.0
- Update prometheus/client_golang to 1.11.1
- Add initialDelay
- Release v1.0.0
- Add preamble.json to release targets
- Update OCP release branch
- Add OpenShift subscription annotation
- Ensure we update the ocp-0.1 branch when releasing
- Release v0.1.32
- make: Small file-based catalog cleanup
- Makefile: Update OPM version to 1.20.0
- Fix deployment on OCP 4.6
- Fix make release-images to properly tag `latest`
- Release v0.1.31
- Use go-install to fetch kustomize
- make: Build a file-based catalog
- make: Remove unused variable
- Remove the OPM dependency
- Ignore generated setup files for end-to-end tests
- updates readme for default toleration change
- updates default tolerations to include infra nodes
- Generate bundle
- Fix controller metrics port
- e2e: Add PSP labels to test namespace
- bump vendor, include ginkgo/gomega
- Update PrometheusRule on operator startup
- e2e: Allow running from an existing deployment
- trivial: fix grammatical issue in README.md
- Release v0.1.30
- Restore CSV ownership of daemon ServiceAccount
- Release v0.1.29
- Makefile: push latest catalog for release
- Change daemon RBAC back to Role
- Release v0.1.28
- Create LICENSE
- Makefile: remove unused vars and setup-envtest download
- Makefile: Make uninstall/undeploy targets more robust
- Makefile: Move controller-gen comment
- Makefile: Fix build dir recipe override
- Use ClusterRole and local RoleBinding for daemon permissions
- Makefile: Add CATALOG_DEPLOY_NS variable for make catalog-deploy
- extend grace period for logcompress e2e
- Release v0.1.27
- e2e: Use pod instead of ds for logcompress file toucher
- Release v0.1.26
- Fix bad remote URL
- Add controller-gen to tools.go
- Use an absolute git reference for the upstream repository
- Clarify the image repository used by default in the release process
- make: Add a deploy-local target
- OWNERS: Prune inactive user
- Release v0.1.25
- operator: Set namespace label for alert
- config/ns: Add pod-security.kubernetes.io labels to the namespace
- tests: Expose -skipCleanupOnError as an env variable
- tests: Use busybox from quay to work around dockerhub's rate limit
- Add CSV support for s390x
- Allow running under OLM's AllNamespaces install mode
- Added code changes for image to support ppc64le
- Remove unused test framework code
- Use go 1.17
- Makefile: Add buildah as an option for RUNTIME=
- Update vendor/
- Update operator framework to operator-sdk v1.15
- Use fedora-minimal:latest for container builds
- Release v0.1.24
- Bug 2072058: Use correct data keys for script configMaps
- Release v0.1.23
- Update OWNERS file
- deps: Bump the github.com/prometheus/client_golang dependency
- Break apart the make target for release
- e2e: Add the operator logs to artifacts
- Bug 2049206: Re-create AIDE configMaps with missing owner
- Unset --skip-cleanup-error for e2e
- Use golang:1.16 for upstream builder
- Add FileIntegrityConfig MaxBackups
- CSV: We support FIPS
- Release v0.1.22
- Move aide.reinit to /run
- Release v0.1.21
- Use `oc apply` instead of `oc create` for namespace
- Handle IO error in init database more leniently

## [1.2.0] - 2023-03-06
### Changes
- Update golang version in Dockerfile for builds
- Bump operator-sdk from 1.15.0 to 1.27.0
- chore: go fmt fixes
- Bump golang dependency to 1.19
- Configure dependabot
- Update coreos/ignition/v2 to 2.14.0
- Update prometheus/client_golang to 1.11.1
- Add initialDelay

## [1.0.0] - 2022-11-22
### Changes
- Add preamble.json to release targets
- Update OCP release branch
- Add OpenShift subscription annotation
- Ensure we update the ocp-0.1 branch when releasing
- make: Small file-based catalog cleanup

## [0.1.32] - 2022-10-24
### Changes
- Makefile: Update OPM version to 1.20.0
- Fix deployment on OCP 4.6
- Fix make release-images to properly tag `latest`

## [0.1.31] - 2022-10-17
### Changes
- Use go-install to fetch kustomize
- make: Build a file-based catalog
- make: Remove unused variable
- Remove the OPM dependency
- Ignore generated setup files for end-to-end tests
- updates readme for default toleration change
- updates default tolerations to include infra nodes
- Generate bundle
- Fix controller metrics port
- e2e: Add PSP labels to test namespace
- bump vendor, include ginkgo/gomega
- Update PrometheusRule on operator startup
- e2e: Allow running from an existing deployment
- trivial: fix grammatical issue in README.md

## [Unreleased] - Date TBD

### Changes
- the upstream catalog image is now built using the
  [file format](https://olm.operatorframework.io/docs/reference/file-based-catalogs/)
  replacing the now deprecated SQLite format.
- We added `initialDelay` option to FileIntegrity CRD to allow users to specify
  the initial delay before the first scan is run. This is useful for
  environments where the operator is deployed before cluster is fully ready.

### Fixes

- Modify the release process to ensure the most recent version is tagged with
  the `latest` tag for the upstream
  [operator](https://quay.io/repository/file-integrity-operator/file-integrity-operator),
  [catalog](https://quay.io/repository/file-integrity-operator/file-integrity-operator-catalog),
  and
  [bundle](https://quay.io/repository/file-integrity-operator/file-integrity-operator-bundle)
  repositories available through quay.io.

### Internal Changes

- Update `make kustomize` to use `go install` for installing kustomize v4. This
  is [necessary](https://github.com/openshift/file-integrity-operator/issues/287)
  for installing kustomize using golang 1.18.

## [0.1.30] - 2022-07-25

### Fixes

- Fixed an [issue](https://bugzilla.redhat.com/show_bug.cgi?id=2109153) where
  upgrades from older versions (0.1.24) would fail due to incorrect ownership of
  the File Integrity Operator service accounts. We recommend users upgrade to
  version 0.1.30 to avoid the issue.

## [0.1.29] - 2022-07-19
### Changes

### Fixes

- Restore role bindings for `file-integrity-daemon` to use `Role` instead of
  `ClusterRole`. Using `ClusterRole` inadvertently broke during upgrades due to
  expectations by Operator Lifecycle Manager. No action is required to consume
  this fix besides upgrading to 0.1.29. Please see the [bug
  report](https://bugzilla.redhat.com/show_bug.cgi?id=2108475) for more
  details.

### Internal Changes

- The `make release-images` target was updated to publish new catalog images
  with each release to
  [quay.io/file-integrity-operator/file-integrity-operator-catalog](https://quay.io/repository/file-integrity-operator/file-integrity-operator-catalog).

## [0.1.28] - 2022-07-14

### Fixes

- Improved contributor experience by making cleanup `make` targets more robust.
- Fixes a [bug](https://bugzilla.redhat.com/show_bug.cgi?id=2104897) where the
  operator isn't able to install into namespaces other than
  `openshift-file-integrity`.
- Expose a new environment variable used by `make catalog-deploy` to deploy the
  operator in a non-default namespace.
- The operator now uses the appropriate labels to prevent
  [warnings](https://bugzilla.redhat.com/show_bug.cgi?id=2088201) about
  elevated privileges.

### Internal Changes

- Minor documentation fixes clarifying details of the release process.
- Removed unused `setup-envtest` dependency required by older versions of
  Operator SDK. This is no longer needed since upgrading the Operator SDK
  version.
- Removed unnecessary `make` targets for build directories since `build/` is
  tracked as part of the repository.
- Increased the grace period for end-to-end tests, giving setup functions
  enough time to prepare test environments, reducing transient test failures.
- End-to-end tests now use a pod for generating files for test data instead of
  a daemon set to improve test speeds.

### Documentation

- The File Integrity Operator now explicitly contains an Apache 2.0 License.

## [0.1.27] - 2022-07-06

### Fixes

- The operator now sets the appropriate namespace for `NodeHasIntegrityFailure`
  alerts. This helps users understand what component is raising the alert.
  Please see the corresponding [bug
  report](https://bugzilla.redhat.com/show_bug.cgi?id=2101393) for more
  details.

## [0.1.26] - 2022-07-01

### Internal Changes

- Included `controller-gen` as a dependency using tools.go.
- Removed remote naming assumptions from `Makefile` release targets, making the
  release process more consistent.
- Added a `deploy-local` target that deploys the operator using the cluster's
  image registry. This is primarily for development purposes.
- Multiple fixes to help stabilize end-to-end testing.

## [0.1.25] - 2022-06-28

### Enhancements

- Added support for using the File Integrity Operator on s390x and ppc64le
  architectures.

### Internal Changes

- Updated operator framework to use operator-sdk version 1.15.0.
- Added formal support for watching all namespaces using `AllNamespaces` in a
  backwards compatible way to adhere to changes required by the operator-sdk
  update.
- Removed unused test framework code that was obsolete after updating the
  operator-sdk framework dependency.
- Added additional flexibility to the container build process to support
  building content across different operating systems.
- Upgraded golang dependency to version 1.17

## [0.1.24] - 2022-04-20
### Changes
- Bug 2072058: Use correct data keys for script configMaps
- Update OWNERS file

## [0.1.23] - 2022-03-31
### Changes
- deps: Bump the github.com/prometheus/client_golang dependency
- Break apart the make target for release
- e2e: Add the operator logs to artifacts
- Bug 2049206: Re-create AIDE configMaps with missing owner
- Unset --skip-cleanup-error for e2e
- Use golang:1.16 for upstream builder
- Add FileIntegrityConfig MaxBackups
- CSV: We support FIPS

## [0.1.22] - 2022-01-13
### Changes
- Move aide.reinit to /run

## [0.1.21] - 2021-10-26
### Changes
- Use `oc apply` instead of `oc create` for namespace
- Handle IO error in init database more leniently

## [0.1.20] - 2021-10-05
### Changes
- Makefile: Make image bundle depend on $TAG
- Fix nil deref in daemonSet upgrade path

## [0.1.19] - 2021-09-24
### Changes
- Delete old aide-ds- prefixed daemonSets if they exist
- Makefile: Add test-catalog targets
- Makefile: Apply monitoring resources during deploy-local
- Makefile: Rename IMAGE_FORMAT var
- Use ClusterRole/ClusterRoleBinding for monitoring perms
- Add MCO and CVO related config excludes
- adapt prometheusrule to only alert on currently existing nodes

## [0.1.18] - 2021-08-20
### Changes
- Optimize per-node reinit calls
- Handle metrics service during operator start
- Bug 1862022: Per-node reinit during update
- Update Dockerfile.ci goland and base image
- Use fedora-minimal:34 base image

## [0.1.17] - 2021-08-05
### Changes
- Move test-specific permissions out of deploy manifests
- Use latest for CSV documentation link
- Enable TLS for controller metrics
- vendor deps
- Add controller-based Prometheus metrics
- Update ignition and MCO dependencies
- Update gosec and fix warnings

## [0.1.16] - 2021-06-02
### Changes
- Handle AIDE error code 255
- Update dependencies
- README: Fix instructions to install from OLM
- Add operator and aide-ds pod limits
- daemon: Handle SIGTERM and SIGKILL
- Add an initial CHANGELOG.md and make changelog target
- Exclude the CNI plugin directory

## [0.1.15] - 2021-05-14
### Changes
- daemon rewrite and other fixes
- Clean up a test log line
- Add nodeSelector to e2e replacement config
- Remove unused channel for logCollectorMainLoop()
- Compression fixes
- Remove the old AIDE container
- Update CSV links
- Add FileIntegrityStatus and NodeIntegrityStatus events
