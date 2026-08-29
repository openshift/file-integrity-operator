---
name: release-fio
description: Cut a file-integrity-operator release — the two-PR backport + release dance for z-stream (1.3.X), or the new-branch + .tekton setup for y-stream (1.4.0). Use when the user asks to release, tag, cut a new FIO version, or create a new release branch.
---

# Release file-integrity-operator (z-stream 1.3.X)

## What actually happens (from PRs #786, #793, #846, #849)

A z-stream release is **two PRs** on `release-1.3`, not one:

1. **Backport PR** — cherry-picks master fixes onto `release-1.3`.
2. **Release PR** — bumps version in a fixed set of files.

Konflux handles downstream image/bundle rebuilds via `chore(deps): update file-integrity-operator-release-1-3 to <sha>` PRs after merge.

The `Makefile` has `prepare-release` / `push-release` / `release-images` targets, but **recent releases have diverged** from them (see "Makefile vs practice" below). Do not blindly run `make push-release`.

## Preconditions

- Maintainer with push access to `openshift/file-integrity-operator` and `quay.io/file-integrity-operator`.
- `IMAGE_REPO` and `TAG` are **unset** in the environment.
- All fixes intended for this release are already merged on `master` with `CMP-NNNN:` or `OCPBUGS-NNNNN:` titles (both satisfy `jira/valid-reference`).
- `VERSION=1.3.X` is set (semver, e.g. `1.3.9`).

### Preflight — clear the queue on the target branch

Before the backport and release PRs go out, flush both of these on the branch you're releasing from (`release-1.3` for a z-stream):

1. **Refresh Go dependencies.** Land a `Update all Go dependencies to latest versions` PR (see `b309f11b6`, or `7ca340fa4` when a Go toolchain bump is bundled). Workflow: `go get -u ./... && go mod tidy && go mod vendor`, then `make test-unit && make verify`, commit and merge. Shipping a release on stale deps reopens CVE exposure.
2. **Merge every open Konflux / Tekton pipeline PR** on the target branch:
   - `gh pr list --repo openshift/file-integrity-operator --base release-1.3 --state open`
   - Merge pending `Update Konflux references`, `chore(deps): update file-integrity-operator-release-1-3 to <sha>`, and any `Re-add ... pipeline customization` follow-ups.
   - Leaving them open means the release lands onto a branch whose pipeline config will race the next bot refresh, and FIO customizations (hermetic, arch list, prefetch, build-nudge) may silently drop during the release window.

Only after both queues are empty (or an open item is explicitly deferred for a documented reason) should Phase 1 proceed.

## Phase 1 — Backport PR

Cherry-pick the master fixes that should ship.

```bash
git fetch origin
git checkout release-1.3
git pull --ff-only
git checkout -b backport-1.3.<N>-fixes

# For each merged master PR, cherry-pick its merge commit:
git cherry-pick -x -m 1 <merge-sha>    # -m 1 for merge commits only
# (for squash-merged commits, drop -m 1)

make test-unit
make verify
git push -u origin backport-1.3.<N>-fixes
```

Open the PR (body template from #846):

```bash
gh pr create --repo openshift/file-integrity-operator \
  --base release-1.3 \
  --title "Backport fixes for 1.3.<N> release" \
  --body "Cherry-picking the commit fixing these issues:
[CMP-NNNN](https://issues.redhat.com/browse/CMP-NNNN): <original title> (#<master-pr>)
[CMP-MMMM](https://issues.redhat.com/browse/CMP-MMMM): <original title> (#<master-pr>)"
```

Wait for merge before Phase 2.

## Phase 2 — Release PR

Only after the backport PR is merged.

```bash
git checkout release-1.3
git pull --ff-only
git checkout -b release-1.3.<N>         # NOTE: no "v" prefix (see divergence below)
```

Touch exactly these files (from the 1.3.8 diff, commit `99ba9364a`):

| File | Change |
|---|---|
| `version/version.go` | `Version = "1.3.<N>"` |
| `version.Makefile` | `VERSION?=1.3.<N>` |
| `build/Dockerfile.openshift` | `version=1.3.<N>` LABEL |
| `bundle.openshift.Dockerfile` | `FIO_OLD_VERSION="1.3.<N-1>"`, `FIO_NEW_VERSION="1.3.<N>"` |
| `bundle/manifests/file-integrity-operator.clusterserviceversion.yaml` | `name: file-integrity-operator.v1.3.<N>`, `olm.skipRange: '>=1.0.0 <1.3.<N>'`, `version: 1.3.<N>`, `replaces: file-integrity-operator.v1.3.<N-1>` |
| `catalog/preamble.json` | entry `name` + `skipRange` |
| `config/manifests/bases/file-integrity-operator.clusterserviceversion.yaml` | `olm.skipRange` (may or may not need bumping — check current value; in 1.3.8 it lagged and was corrected in the release PR) |

You can drive most of this via the Makefile (but staged, not pushed — see divergence):

```bash
make update-skip-range VERSION=1.3.<N>   # rewrites the CSV/preamble skip ranges
make bundle VERSION=1.3.<N>              # regenerates bundle/manifests/
# Then hand-edit version/version.go, version.Makefile, build/Dockerfile.openshift,
# bundle.openshift.Dockerfile (or use sed).
```

Commit as **one** commit:

```bash
git add version/version.go version.Makefile build/Dockerfile.openshift \
  bundle.openshift.Dockerfile bundle/manifests catalog/preamble.json \
  config/manifests/bases
git commit -m "Release 1.3.<N>

Tag a new z-stream release to <reason: e.g. address CVE-YYYY-NNNNN and CMP-NNNN>."
git push -u origin release-1.3.<N>
```

Open the PR:

```bash
gh pr create --repo openshift/file-integrity-operator \
  --base release-1.3 \
  --title "Release 1.3.<N>" \
  --body "Tag a new z-stream release to <reason>."
```

Merge gates: `approved` + `lgtm` + typically `qe-approved`.

## Phase 3 — After merge

- Tag the release:

  ```bash
  git fetch origin
  git checkout release-1.3
  git pull --ff-only
  git tag v1.3.<N>
  git push origin v1.3.<N>
  ```

- `make release-images VERSION=1.3.<N>` pushes versioned + `latest` tags to `quay.io/file-integrity-operator/file-integrity-operator{,-bundle,-catalog}`. Requires Quay write access.
- Konflux bot opens one or more `chore(deps): update file-integrity-operator-release-1-3 to <sha>` PRs (e.g. #848, #850). Merge these; they wire the new SHA into downstream bundle/catalog builds.
- OSBS/Konflux does the downstream Red Hat image build via `.tekton/` pipelines and `build/Dockerfile.openshift`.

## Makefile vs practice

The Makefile documents a `make prepare-release / push-release / release-images` pipeline. Recent releases diverge:

- `make push-release` commits as `Release v<TAG>` and creates branch `release-v<TAG>` (with `v`). Recent PRs use `Release 1.3.X` and `release-1.3.X` (no `v`).
- `make push-release` also merges to `ocp-1.0` and pushes it — this looks legacy; recent z-stream releases land via PR on `release-1.3`.
- `make prepare-release` stages `CHANGELOG.md`, but PRs #786 and #849 didn't update it. (Last CHANGELOG entry is 1.3.4.)

Safe play: use the Makefile for `update-skip-range` and `bundle` (mechanical edits), but do the branch/commit/PR dance manually as above.

## Watch-outs

- **`config/manifests/bases/…` skipRange drift** — in #849 this file jumped from `<1.3.5-dev` to `<1.3.8`, meaning it had been missed in earlier releases. Always verify it matches the new version.
- **`bundle.openshift.Dockerfile` has dual ARGs** (`FIO_OLD_VERSION`, `FIO_NEW_VERSION`) — both must be bumped, and `FIO_OLD_VERSION` must equal the previous release.
- **`build/Dockerfile.openshift` version label** — easy to miss; fixed in #753 after it was left blank in a past release.
- **Don't retag** — if an image needs to change, bump to the next patch version. Released tags are immutable.

## `.tekton/` during a z-stream release

**Nothing.** Release PRs #786, #849 don't modify any `.tekton/*.yaml`. Konflux builds fire automatically on each push to `release-1.3` through the existing `file-integrity-operator-release-1-3-{pull-request,push}.yaml` + `file-integrity-operator-bundle-release-1-3-{pull-request,push}.yaml` pipelines.

The operator push pipeline carries `build.appstudio.openshift.io/build-nudge-files: "bundle-hack/update_csv.go"` — after the operator image builds, Konflux re-runs `update_csv.go` against the bundle component and produces the `chore(deps): update file-integrity-operator-release-1-3 to <sha>` follow-up PRs that bump the CSV image SHA automatically. **Don't remove that annotation; don't manually edit CSV image SHAs on release-1.3.**

---

# Cutting a new release branch (y-stream, e.g. 1.4.0)

This is the scenario where `.tekton/` changes are required. Procedure reconstructed from `ce4525e4a6` (branch init), `28c7d9d66` (rename), `36b22b2ba` (delete stale), `8bdbe36b8` (re-add customizations).

## 0. Preflight on master

Before branching, clear the same two queues on `master` that Phase 1 clears on `release-1.3` for z-streams: merge a fresh `Update all Go dependencies to latest versions` PR, and merge every open Konflux / Tekton pipeline PR. Cutting `release-1.4` off a master that still has pending pipeline refreshes inherits the staleness into the new branch and creates avoidable merge churn during the first weeks of the new stream.

## 1. Branch off master

```bash
git fetch origin
git checkout -b release-1.4 origin/master
```

## 2. Rename the four `.tekton/` files

Master uses `-dev` as the Konflux component suffix. Copy and rename to the new branch suffix (dashes in filenames, dots only in `target_branch` CEL values):

- `file-integrity-operator-dev-pull-request.yaml` → `file-integrity-operator-release-1-4-pull-request.yaml`
- `file-integrity-operator-dev-push.yaml` → `file-integrity-operator-release-1-4-push.yaml`
- `file-integrity-operator-bundle-dev-pull-request.yaml` → `file-integrity-operator-bundle-release-1-4-pull-request.yaml`
- `file-integrity-operator-bundle-dev-push.yaml` → `file-integrity-operator-bundle-release-1-4-push.yaml`

## 3. Edit inside each file

Replace every identifier that embeds the branch:

| Field | From | To |
|---|---|---|
| `metadata.name` | `file-integrity-operator[-bundle]-dev-on-{pull-request,push}` | `...-release-1-4-on-{pull-request,push}` |
| `labels.appstudio.openshift.io/application` | `file-integrity-operator-dev` | `file-integrity-operator-release-1-4` |
| `labels.appstudio.openshift.io/component` | `file-integrity-operator[-bundle]-dev` | `file-integrity-operator[-bundle]-release-1-4` |
| `on-cel-expression` `target_branch ==` match | `"master"` | `"release-1.4"` (dots) |
| Bundle-path CEL regex fragment `-bundle-dev-.*\\.yaml` | `-dev-` | `-release-1-4-` |
| `spec.params.output-image` | `.../file-integrity-operator[-bundle]-dev:...` | `.../file-integrity-operator[-bundle]-release-1-4:...` |
| `spec.taskRunTemplate.serviceAccountName` | `build-pipeline-file-integrity-operator[-bundle]-dev` | `build-pipeline-file-integrity-operator[-bundle]-release-1-4` |

## 4. Re-add FIO-specific customizations

Konflux stock templates drop these. They must be explicitly re-added every time the bot refreshes a template (see `8bdbe36b8`):

- `spec.params.build-platforms: [linux/x86_64, linux/ppc64le, linux/s390x]` — **no arm64** (removed in `c24ef5f54`).
- `spec.params.hermetic: "true"`
- `spec.params.build-source-image: "true"`
- `spec.params.prefetch-input: '[{"type": "rpm", "path": "konflux"}, {"type": "gomod", "path": "."}]'`
- On the `prefetch-dependencies` task: param `dev-package-managers: "true"`.
- On the operator push pipeline: the `ADDITIONAL_TAGS: ['{{ target_branch }}']` stanza in the `push-dockerfile` finally block (from PR #795).
- On the operator push pipeline: annotation `build.appstudio.openshift.io/build-nudge-files: "bundle-hack/update_csv.go"`.

## 5. Delete stale `.tekton/*.yaml`

If the new branch carries any `file-integrity-operator-*-{dev,master,release-1-3}-*.yaml`, delete it — those will cross-fire against the wrong Konflux application. Commit `36b22b2ba` did exactly this on release-1.3.

## 6. Konflux admin setup (out-of-band)

- Provision Konflux application `file-integrity-operator-release-1-4` with both components.
- Create `build-pipeline-file-integrity-operator[-bundle]-release-1-4` service accounts.
- Configure the Pipelines-as-Code webhook for the new branch.

These happen in the Konflux UI / tenant admin flow (`ocp-isc-tenant`), not in this repo. Coordinate with whoever owns the Konflux tenant before pushing the branch.

## 7. Verify

Push a trivial commit to `release-1.4` and confirm both operator and bundle pipelines fire, producing images at `quay.io/redhat-user-workloads/ocp-isc-tenant/file-integrity-operator[-bundle]-release-1-4:*`.

---

# Ongoing `.tekton/` maintenance (any branch)

Konflux bot raises periodic `Update Konflux references` and `chore(deps): …` PRs that refresh pipeline template SHAs. These refreshes routinely drop the customizations from step 4 above. Commits titled "Re-add operator pipeline customization" / "Readd bundle pipeline customizations" show this is a recurring chore, not a one-time setup. On every bot PR, diff against the prior state and re-add anything dropped (usually in a fast-follow commit on the same PR).

**Trigger separation:** the operator and bundle pipelines carry inverted CEL expressions on the bundle paths `^bundle/`, `^bundle-hack/`, `^bundle\.openshift\.Dockerfile`, `^\.tekton/file-integrity-operator-bundle-*-.*\.yaml`. Operator pipeline runs when those paths **didn't** change; bundle pipeline runs **only** when they did. Adding a new bundle-impacting path means updating both CEL lists.
