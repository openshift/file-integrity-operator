---
name: e2e-triage
description: Diagnose or run a file-integrity-operator e2e test — know how the operator is deployed during `make e2e`, scope a single test, point it at a feature-branch image, inspect operator/DaemonSet logs and result ConfigMaps. Use when the Prow `e2e-aws` / `e2e-bundle-aws` job fails, or when reproducing and validating against a real cluster.
---

# Triage or run a file-integrity-operator e2e test

## How the operator is deployed during `make e2e`

The test harness installs the operator itself unless you explicitly hand it a pre-installed bundle. Flow (driven by `Makefile` + `tests/framework/` + `tests/e2e/helpers.go`):

1. **`make e2e-set-image`** writes the operator image pullspec into `config/manager/kustomization.yaml`.
   - If `IMAGE_FROM_CI` is set → that wins (Prow uses this).
   - Else → `$(IMG)` = `$(IMAGE_TAG_BASE):$(TAG)`, default `quay.io/file-integrity-operator/file-integrity-operator:latest`.
2. **`make prep-e2e`** kustomize-builds two manifests into `tests/_setup/`:
   - `crd.yaml` (from `config/crd`)
   - `deploy_rbac.yaml` (from `config/e2e`, which is `config/rbac` + `config/manager` — no namespace, no CRD)
3. **`go test ./tests/e2e`** runs with `-root=$(PROJECT_DIR) -globalMan=tests/_setup/crd.yaml -namespacedMan=tests/_setup/deploy_rbac.yaml -skipCleanupOnError=$(E2E_SKIP_CLEANUP_ON_ERROR) [--platform openshift|rosa]`.
4. Inside the test, `setupFileIntegrityOperatorCluster` (`tests/e2e/helpers.go:467`):
   - rewrites the literal `openshift-file-integrity` to the actual test namespace in `deploy_rbac.yaml`,
   - if `TEST_BUNDLE_INSTALL` **not** set → `ctx.InitializeClusterResources` applies the CRD + RBAC + operator `Deployment` from those manifests,
   - if `TEST_BUNDLE_INSTALL` **is** set (any non-empty value) → assumes the operator is already installed (e.g. via OLM bundle) and skips creation,
   - seeds the metrics-scrape RBAC (ClusterRole, ClusterRoleBinding, SA token Secret),
   - waits for `deploy/file-integrity-operator` to become Available.

## Environment variables that matter

| Var | Purpose | Default |
|---|---|---|
| `IMG` | operator image pullspec used by `e2e-set-image` | `$(IMAGE_TAG_BASE):$(TAG)` |
| `IMAGE_FROM_CI` | overrides `IMG`; Prow sets this from its build | unset |
| `E2E_GO_TEST_FLAGS` | passed to `go test` | `-v -timeout 90m` |
| `E2E_SKIP_CLEANUP_ON_ERROR` | keep failing-test state for inspection | `true` |
| `TEST_OPERATOR_NAMESPACE` | namespace the operator deploys into | kubeconfig default |
| `TEST_WATCH_NAMESPACE` | namespace the operator watches | matches operator namespace |
| `TEST_BUNDLE_INSTALL` | skip operator install, assume bundle already on cluster | unset |

## Validating a feature branch

Four paths, fastest to slowest feedback:

### 1. Targeted single test against an existing image

Fastest loop when iterating on one scenario:

```bash
# if IMG is already right (from make push):
E2E_GO_TEST_FLAGS="-v -timeout 20m -run TestFileIntegrityPriorityClassName" make e2e
```

### 2. Your fork's image from Quay (or any external registry)

```bash
export IMAGE_REPO=quay.io/<your-user>
export TAG=$(git rev-parse --short HEAD)
make images            # builds operator + bundle
make push              # pushes to $IMAGE_REPO/file-integrity-operator{,-bundle}:$TAG
make e2e IMG=$IMAGE_REPO/file-integrity-operator:$TAG
```

### 3. OpenShift in-cluster registry (no external push)

Handy on a personal cluster with no Quay credentials:

```bash
make deploy-local
# under the hood: make install + make image-to-cluster (pushes to the cluster's
# internal registry as image-registry.openshift-image-registry.svc:5000/openshift/
# file-integrity-operator:$TAG) + make deploy with that pullspec.
make e2e    # test framework will see the already-deployed operator
```

Note: `deploy-local` patches `config/manager/deployment.yaml` in place and reverts it; if the revert fails, `git restore config/manager/deployment.yaml config/manager/kustomization.yaml`.

### 4. Against an OLM-installed bundle

When you've installed the operator through a catalog (mirrors what customers do):

```bash
TEST_BUNDLE_INSTALL=1 \
  TEST_WATCH_NAMESPACE=openshift-file-integrity \
  TEST_OPERATOR_NAMESPACE=openshift-file-integrity \
  make e2e
```

### 5. Prow `e2e-aws` on the PR (what landing cares about)

The authoritative signal for merge:

```bash
gh pr comment <PR> -R openshift/file-integrity-operator -b "/test e2e-aws"
```

Prow will build the image from your branch HEAD and run the full suite. Logs via the `prow-logs` skill.

## Scope a single test locally

The suite is ~90 min. Scope with the go test `-run` flag (test names in `tests/e2e/e2e_test.go` all start with `TestFileIntegrity` or `TestMetrics` / `TestServiceMonitoring`):

```bash
E2E_GO_TEST_FLAGS="-v -timeout 20m -run TestFileIntegrityConfigurationStatus" make e2e
```

Force cleanup on failure (the default `true` *skips* cleanup so state is inspectable):

```bash
E2E_SKIP_CLEANUP_ON_ERROR=false make e2e
```

For ROSA: `make e2e-rosa` passes `--platform rosa` which skips MachineConfig-related operations and schemes.

## Inspect live state

All resources in `openshift-file-integrity` (or your `TEST_OPERATOR_NAMESPACE`):

```bash
# Operator
oc -n openshift-file-integrity get deploy,pods -l name=file-integrity-operator
oc -n openshift-file-integrity logs deploy/file-integrity-operator

# AIDE DaemonSets (one per FileIntegrity CR, plus short-lived reinit DS)
oc -n openshift-file-integrity get ds,pods -l app=aide-ds-<fileintegrity-name>
oc -n openshift-file-integrity logs ds/aide-ds-<fileintegrity-name> -c aide

# CRs
oc get fileintegrities.fileintegrity.openshift.io -A
oc get fileintegritynodestatuses -A

# Events
oc -n openshift-file-integrity get events --field-selector reason=FileIntegrityStatus
oc -n openshift-file-integrity get events --field-selector reason=NodeIntegrityStatus
```

## Read the failure log

Failure details live in a result ConfigMap linked from the `FileIntegrityNodeStatus`:

```bash
NS=openshift-file-integrity
oc get fileintegritynodestatus/<name> -n $NS -o yaml   # find resultConfigMapName
oc get cm/<result-cm> -n $NS -o jsonpath="{ .data.integritylog }"
```

If the ConfigMap carries annotation `file-integrity.openshift.io/compressed`, decode:

```bash
oc get cm/<result-cm> -n $NS -o jsonpath="{ .data.integritylog }" | base64 -d | gunzip
```

## Metrics sanity check

```bash
oc run --rm -i --restart=Never \
  --image=registry.fedoraproject.org/fedora-minimal:latest \
  -n openshift-file-integrity metrics-test -- bash -c \
  'curl -ks -H "Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)" https://metrics.openshift-file-integrity.svc:8585/metrics-fio' | grep file_integrity
```

## From Prow artifacts

For PR-job failures, fetch operator + aide-ds pod logs from `artifacts/e2e-aws/test/artifacts/` and `gather-extra/artifacts/pods/`. Use the `prow-logs` skill to derive the URLs.

## Known failure shapes

- **Mount-propagation errors (CSI + multipath)** — expect `mountPropagation: HostToContainer` (#424, OCPBUGS-14947).
- **False positives after scaling a node** — MCO annotation files / kubelet CA; default config excludes them (#368, #413, #534).
- **Reinit stuck** — `holdoff` annotation race; per-node holdoff fixes it (#339). Check `file-integrity.openshift.io/node-holdoff-*` annotations on the FileIntegrity.
- **FIPS guard termination** — LD_PRELOAD MD5 guard working as intended (#660, OCPBUGS-56409). Exit code `64` = `MD5_GUARD_ERROR`.
- **Daemon starts before metrics secrets exist** — retry logic added in #821 (CMP-3757) and #845.
