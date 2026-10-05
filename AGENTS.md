# file-integrity-operator

OpenShift Operator that runs AIDE file-integrity scans on cluster nodes via a privileged DaemonSet. Module path: `github.com/openshift/file-integrity-operator`. Dependencies are vendored (`-mod=vendor`).

Code layout:

- `pkg/apis/fileintegrity/v1alpha1/` — CRD types (`FileIntegrity`, `FileIntegrityNodeStatus`). CR group: `fileintegrity.openshift.io/v1alpha1`.
- `pkg/controller/{fileintegrity,configmap,node,status,metrics}/` — one reconciler per package.
- `pkg/common/` — shared constants, helpers. `constants.go` holds every annotation/label key and AIDE error code used across the codebase.
- `cmd/manager/` — a single Cobra app with two subcommands. `operator.go` registers `OperatorCmd` (controller-runtime manager); `daemon.go` registers `DaemonCmd` (runs per-node in the DaemonSet, drives the AIDE loop and the log-collector loop via `loops.go` + `logcollector_util.go`). Only these two subcommands are wired in `main.go`; `build/bin/entrypoint` is a trivial `exec ${OPERATOR} $@` wrapper.
- `tests/e2e/` — e2e tests; framework in `tests/framework/`.
- `bundle-hack/` — **downstream build-time tooling, not runtime.** `update_csv.go` rewrites the CSV during the Konflux build (used for image-SHA pinning). `update_bundle_annotations.sh` writes OCP version / channel into `bundle/metadata/annotations.yaml`. `rpms.lock.yaml` pins RPMs for Konflux hermetic builds. Invoked from `.tekton/` pipelines, not from `make`.

## Sources of truth

Read from these files rather than duplicating their contents anywhere:

- `go.mod` — Go version and direct dependencies.
- `Makefile`, `version.Makefile` — build targets, `VERSION`, tool versions (operator-sdk, kustomize).
- `PROJECT` — kubebuilder scaffold (resources, API groups).
- `OWNERS` — approvers and reviewers.
- `pkg/common/constants.go` — all annotation/label keys and AIDE error codes. Never hardcode strings like `"file-integrity.openshift.io/re-init"`; use the constant.

## Before finishing any change

- `make test-unit` (fmt + vet + unit tests) and `make verify` (vet + `gosec` at severity/confidence ≥ medium). These mirror the required Prow jobs `unit` and `verify`.
- Edited `pkg/apis/fileintegrity/v1alpha1/*`? Run `make manifests` (CRDs) and `make generate` (DeepCopy).
- Edited CSV, RBAC, or anything that flows into the OLM bundle? Run `make bundle`, then `hack/tree-status` — drift fails the Prow `verify` job. `make bundle` also rewrites `config/manager/kustomization.yaml` as a side effect; the Makefile restores it via `git restore`, but double-check it's clean before committing. To pin the CSV image: `make bundle IMG=quay.io/file-integrity-operator/file-integrity-operator:<version>`.
- Dependency changes: `go mod tidy && go mod vendor`. Never edit `vendor/` by hand.

## Generated / don't-hand-edit

Always regenerate, never edit directly:

- `bundle/manifests/` and `bundle/metadata/annotations.yaml` — from `make bundle`.
- `config/crd/bases/fileintegrity.openshift.io_*.yaml` — from `make manifests`.
- `config/rbac/role.yaml` — from `make manifests` (driven by `+kubebuilder:rbac:` markers).
- `pkg/apis/fileintegrity/v1alpha1/zz_generated_*.go` — from `make generate`.
- `vendor/` — from `go mod vendor`.

## Types and kubebuilder markers

- **Two CRs, deliberately split.** `FileIntegrity` (cluster-scoped config, `.Status.Phase` only) lives alongside `FileIntegrityNodeStatus` (per-node scan results, owns `Results[]` + `LastResult`). Don't stuff per-node status into `FileIntegrity.Status`; use a new `FileIntegrityNodeStatus` or extend the existing scan-result fields.
- **Phase/condition vocabulary is fixed.** `FileIntegrityStatusPhase` is one of `Initializing | Active | Pending | Error`. `FileIntegrityNodeCondition` is `Succeeded | Failed | Errored`. Don't invent new values; consumers (metrics, events, alerts) key on these exact strings.
- **RBAC markers are centralized** in `pkg/controller/fileintegrity/setup.go`, above the `FileIntegrityReconciler.Reconcile` comment block. All controller RBAC needs live there — do not scatter `+kubebuilder:rbac:` comments across individual reconcilers, even though tooling would accept it. Adding a new permission means editing that single file, then `make manifests`.
- **CRD markers** (`+kubebuilder:validation:…`, `+kubebuilder:default=…`, `+kubebuilder:printcolumn:…`, `+kubebuilder:subresource:status`) sit on type fields in `pkg/apis/fileintegrity/v1alpha1/fileintegrity_types.go` and drive `config/crd/bases/` via `make manifests`. Defaults belong in the marker, not in controller logic — see existing `+kubebuilder:default=900` on `GracePeriod` and the structured `Tolerations` default for reference.

## Controller patterns

- **Reconciler skeleton is split across two files per package.** `setup.go` holds the struct definition, the `Reconcile` interface-matching wrapper, and `SetupWithManager`; the sibling `*_controller.go` file holds the real reconcile logic (called via a helper like `r.ConfigMapReconcile(req)` or `r.FileIntegrityControllerReconcile(req)`). Follow this split for new reconcilers. The `_ = ctrlLog.FromContext(ctx)` line in every `setup.go` is kubebuilder scaffold noise; don't propagate it into new logic — the codebase logs through package-scoped `logf.Log.WithName(...)` vars instead.
- **Three-function wire-up per controller package.** Every controller exposes an exported `Add<Name>Controller(mgr, *metrics.Metrics) error` that `cmd/manager/operator.go` calls, plus two private helpers: `new<Name>Reconciler` builds the struct with `Client`, `Scheme`, `Recorder`, `Metrics`; `add<Name>Controller` wires `NewControllerManagedBy(mgr).Named("<name>-controller").For(&PrimaryType).Watches(…).Complete(r)`. Follow this shape; don't collapse the three functions.
- **Secondary-resource watches use a mapper in a sibling file.** When a reconciler needs to react to events on a different type (FileIntegrity watching ConfigMap, for example), put the translator in `<name>_mapper.go` implementing `handler.MapFunc` (see `fileintegrity_cm_mapper.go` for the template). Mapper lists the primary CRs and returns `[]reconcile.Request` for the ones affected.
- **Reconcile returns use `reconcile.Result`** (from `sigs.k8s.io/controller-runtime/pkg/reconcile`), not `ctrl.Result`, for consistency with existing code. Canonical forms: `return reconcile.Result{}, nil` (done), `return reconcile.Result{}, err` (controller-runtime requeues with backoff), `return reconcile.Result{Requeue: true}, nil`, `return reconcile.Result{RequeueAfter: <d>}, nil`.
- **Event recorder is per-reconciler.** Each reconciler struct holds `Recorder record.EventRecorder`, initialized at setup via `mgr.GetEventRecorderFor("<ctrlname>")` (see `fileintegrity_controller.go:52`, `configmap_controller.go:43`, `status_controller.go:33`). Emit with `r.Recorder.Eventf(obj, eventType, reason, format, args…)` directly — there is no `pkg/common` wrapper. Reason strings are short PascalCase nouns like `FileIntegrityStatus`, `NodeIntegrityStatus`, `PriorityClass`; don't invent per-case reasons.
- **Metrics go through a shared `*metrics.Metrics` struct** attached to reconcilers; increment via named methods like `r.metrics.IncFileIntegrityPhaseActive()`. Register new metrics in `pkg/controller/metrics/metrics.go` with names constructed from the `metricNamespace = "file_integrity_operator"` prefix; they expose on `/metrics-fio` (port `8585`). The counterfeiter-generated `metricsfakes/fake_impl.go` is committed but its `go:generate` line is commented out — if you change the `impl` interface, regenerate by hand with `go run github.com/maxbrunsfeld/counterfeiter/v6 -generate`.
- **Status updates are single-shot** — `r.client.Status().Update(ctx, deepCopy)` without `RetryOnConflict` wrapping (see `status_controller.go:172`). If you need conflict handling for a new hot-path update, introduce it deliberately.

## Operator ↔ daemon runtime architecture

The same binary runs in two modes (cobra subcommands `operator` and `daemon`). They communicate through three deliberately narrow channels — understand these before adding a new signal:

- **Kubernetes CRs.** The operator owns `FileIntegrity` state. The daemon (running per-node in a privileged DaemonSet) reads the FI it belongs to via a dedicated dynamic client in `cmd/manager/daemon.go` — it is **not** a controller-runtime reconciler.
- **Files on the host filesystem.** The operator spawns a **separate reinit DaemonSet** (`reinitAideDaemonset` in `fileintegrity_controller.go`, distinct from the main `aideDaemonset` — two DaemonSets, not one) whose only job is to drop `/hostroot/run/aide.reinit`. The daemon's `reinitLoop` polls for that file. If you change the trigger path, update both the operator and the daemon.
- **Result ConfigMaps.** The daemon's log-collector writes scan results into per-node ConfigMaps in the operator namespace; the `ReconcileConfigMap` reconciler consumes them and creates/updates `FileIntegrityNodeStatus` objects. Results >1MB are gzip-compressed and base64-encoded with a `file-integrity.openshift.io/compressed` annotation (see `pkg/common/constants.go`).

The daemon is cooperating goroutines (`aideLoop`, `reinitLoop`, `holdOffLoop`, `integrityInstanceLoop`, `logCollectorMainLoop` in `cmd/manager/loops.go` + `logcollector_util.go`) coordinated via error channels and a `sync.WaitGroup`. A new background task on the daemon side means a new loop registered in `daemonMainLoop`.

User-provided AIDE configs go through `prepareAideConf` in `pkg/controller/fileintegrity/config.go`, which rewrites `database=`, `database_out=`, `report_url=file:`, `@@define DBDIR`, `@@define LOGDIR`, and prepends `/hostroot` to any absolute path or exclusion (`!/…`). If you add a new path-aware AIDE directive to the defaults, handle it here too.

## Tests

Three styles coexist — match the surrounding files when adding new tests.

- **`cmd/manager/*_test.go` — Ginkgo / Gomega BDD** (`Describe`, `Context`, `When`, `It`, `Expect`). `manager_suite_test.go` is the Ginkgo entry point. Keep new operator-startup / PrometheusRule / webhook-plumbing tests here.
- **`pkg/**/*_test.go` — plain `testing.T`**, optionally with `stretchr/testify/require`. Counterfeiter fakes under `pkg/controller/metrics/metricsfakes/` are consumed by `metrics_test.go` (re-generate by hand if you change the `impl` interface — `go:generate` is intentionally commented out).
- **`tests/e2e/` — plain `testing.T` against a real cluster** via the custom wrapper in `tests/framework/`. Entry point is `setupTest(t)` returning `(*framework.Framework, *framework.Context, namespace)`. Reuse the `waitFor*` / `assert*` / `retryDefault` helpers already in `tests/e2e/helpers.go` — don't re-roll polling helpers (there are 98 of them; pick or extend one).

Although `github.com/onsi/ginkgo` and `gomega` are present transitively, **don't introduce Ginkgo into `pkg/` or `tests/e2e/`** — only `cmd/manager/` uses it.

## File size and placement

Target Go source files under ~500 lines, tests excluded. A file past ~800 lines is a signal that the next feature belongs in a new module, not another function in the same file. Prefer new focused packages over growing the grab-bag packages.

These files are already bloated — **don't extend them unless you're specifically refactoring them smaller.** Put new functionality in a sibling file or package:

- `pkg/controller/fileintegrity/fileintegrity_controller.go` (~1080 LoC) — main reconciler. Split new feature logic into a sibling file (e.g. `fileintegrity_reinit.go`) or a sub-package.
- `pkg/common/util.go` (~570 LoC) — helper grab-bag. Treat `pkg/common` like a constrained library: resist adding to it. New helpers that naturally group (AIDE parsing, daemon helpers, node selection) belong in a focused package under `pkg/` or `pkg/common/<topic>/`.
- `tests/e2e/helpers.go` (~2550 LoC) — e2e helper dumping ground. Split new helpers into topical files (`helpers_reinit.go`, `helpers_metrics.go`, `helpers_config.go`) rather than appending.
- `tests/e2e/e2e_test.go` (~1310 LoC) — main e2e test file. Group related new tests in a focused `*_test.go` file.

Do not create small helper methods referenced only once; inline them.

## AIDE configuration

- Default config is `pkg/controller/fileintegrity/config_defaults.go`. At runtime the operator rewrites `database`, `database_out`, `report_url`, `DBDIR`, and `LOGDIR` to pod-appropriate paths.
- **Two AIDE config variants coexist for forward-compatibility.** The default vars (`DefaultAideConfigCommonStart`, `…End`) target AIDE 0.16 — **this is what production and all shipping downstream images run.** The `*018` vars target AIDE 0.18 and are gated on `AIDE_VERSION=0.18`; they are preparatory only and not in production use yet. If you change the default config, update both variants together so the 0.18 path doesn't silently diverge.
- `mhash` digests are not supported by the AIDE container; use the default `CONTENT_EX` group for digests.
- FIPS is enforced by an LD_PRELOAD MD5 guard (`build/guard/libaide_md5_guard.so`, installed at `/opt/libaide_md5_guard.so` — see `pkg/common/util.go`). It is built only when `libgcrypt-devel` is installed (`HAS_LIBCRYPT_DEV` gate in the Makefile). The guard hard-fails if AIDE tries to use MD5; exit code `64` (`MD5_GUARD_ERROR`) indicates a guard-triggered termination.
- Host filesystem is mounted under `/hostroot/` inside the daemon container. AIDE DB/log live at `/hostroot/etc/kubernetes/aide.db.gz{,.new}` and `/hostroot/etc/kubernetes/aide.log{,.new}`; the reinit trigger file is `/hostroot/run/aide.reinit`.

## Four Dockerfiles — don't confuse them

- `build/Dockerfile` — upstream / local dev (`golang` + `fedora-minimal:37` pinning AIDE 0.16). Entry point `/usr/local/bin/entrypoint`.
- `Dockerfile.ci` — Prow CI (`registry.ci.openshift.org/openshift/release:rhel-9-release-golang-*-openshift-*`) + AIDE 0.16. Used by the `images` presubmit.
- `Dockerfile.AIDE0.18.ci` — **preparatory-only** CI variant for eventual AIDE 0.18 support (not shipped; production and downstream are still 0.16). Installs unpinned `aide`, asserts the version is 0.18, sets `AIDE_VERSION=0.18`. Keep it building so the 0.18 path doesn't rot, but don't rely on it downstream.
- `build/Dockerfile.openshift` — downstream Konflux/OSBS. Uses `brew.registry.redhat.io/rh-osbs/openshift-golang-builder`, sets `BUILD_FLAGS=-tags strictfipsruntime`, requires `libgcrypt-devel`, carries Red Hat container labels. The `version=` LABEL is pinned manually — verify it on every release; stale values have shipped before.

## Dev tools are vendored via `tools.go`

`tools.go` at repo root uses the standard `//go:build tools` pattern to keep `gosec` and `controller-gen` reachable through `go mod vendor`. When a new dev-only binary is needed (linter, codegen tool), add a blank import here and run `go mod tidy && go mod vendor` so the build can resolve it from `vendor/` without a separate install step.

## PR and commit title conventions

- `CMP-NNNN: <summary>` is the current convention and covers **both bug fixes and features** — FIO bugs live in the CMP Jira project now.
- `OCPBUGS-NNNNN: <summary>` is legacy but still accepted for OCP-wide bug tracker items.
- Plain-English titles are fine for changes without a Jira.
- PR body links the Jira (`https://issues.redhat.com/browse/<key>`). Both prefixes satisfy the `jira/valid-reference` Prow label.

## Skills for procedural work

`.claude/skills/` — invoke these instead of rederiving the procedure:

- `prow-logs` — fetch CI artifacts (build logs, JUnit, gather-extra) for a failing Prow presubmit.
- `e2e-triage` — diagnose a failing e2e test; scope a single test; inspect pods/CRs/result ConfigMaps.
- `release-fio` — cut a z-stream 1.3.X release. The Makefile's release targets have drifted from practice; follow the skill, not `make push-release`.
