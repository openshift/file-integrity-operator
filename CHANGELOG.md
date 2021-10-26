# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic
Versioning](https://semver.org/spec/v2.0.0.html).

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
