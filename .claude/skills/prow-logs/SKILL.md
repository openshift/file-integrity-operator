---
name: prow-logs
description: Fetch OpenShift Prow CI artifacts for a file-integrity-operator PR — build logs, JUnit XML, gather-extra cluster artifacts. Use when diagnosing a failing presubmit job on openshift/file-integrity-operator.
---

# Prow logs for file-integrity-operator

## Find the build ID

```bash
gh pr checks <PR> --repo openshift/file-integrity-operator
```

Links go to `https://prow.ci.openshift.org/view/gs/test-platform-results/pr-logs/pull/openshift_file-integrity-operator/<PR>/<job>/<build-id>`. The trailing integer is the build ID.

UI alternative: `https://prow.ci.openshift.org/pr-history?org=openshift&repo=file-integrity-operator&pr=<PR>`.

## Artifact URLs

Base:

```
https://storage.googleapis.com/test-platform-results/pr-logs/pull/openshift_file-integrity-operator/<PR>/<job>/<build-id>/
```

Files worth fetching:

| Path | Purpose |
|---|---|
| `build-log.txt` | test container stdout/stderr. Start here. |
| `finished.json`, `started.json` | pass/fail + timing |
| `artifacts/<job>/<step>/build-log.txt` | per-step logs for multi-step (e2e) jobs |
| `artifacts/<job>/<step>/artifacts/junit*.xml` | JUnit results |
| `artifacts/<job>/gather-extra/artifacts/pods/` | operator + DaemonSet pod logs (e2e only) |
| `artifacts/<job>/gather-extra/artifacts/events.json` | cluster events (e2e only) |

Browse a full run tree: `https://gcsweb-ci.apps.ci.l2s4.p1.openshiftapps.com/gcs/test-platform-results/pr-logs/pull/openshift_file-integrity-operator/<PR>/<job>/<build-id>/`

## Job names

Pattern: `pull-ci-openshift-file-integrity-operator-<branch>-<context>` where `<context>` is:

Required: `unit`, `verify`, `go-build`, `images`, `ci-index-file-integrity-operator-bundle`, `e2e-aws`
Optional: `e2e-bundle-aws`, `e2e-bundle-aws-upgrade`, `e2e-rosa`

`<branch>` is `master` or `release-4.XX` / `release-1.3`. Periodics: `periodic-ci-openshift-file-integrity-operator-master-nightly-4.XX-*`.

## Rerun commands (PR comments)

- `/retest` — all failed required jobs.
- `/retest-required` — only mandatory failures.
- `/test <context>` — one specific job, e.g. `/test e2e-aws`.
- `/ok-to-test` — gate for first-time contributors; org members only.
