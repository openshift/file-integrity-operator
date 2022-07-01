# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic
Versioning](https://semver.org/spec/v2.0.0.html).

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
