# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic
Versioning](https://semver.org/spec/v2.0.0.html).

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
