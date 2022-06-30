# file-integrity-operator
The file-integrity-operator is a OpenShift Operator that continually runs file integrity checks on the cluster nodes. It deploys a DaemonSet that initializes and runs privileged AIDE ([Advanced Intrusion Detection Environment](https://aide.github.io)) containers on each node, providing a log of files that have been modified since the initial run of the DaemonSet pods.

### Deploying:

To deploy the operator using the latest released file-integrity-operator image available on quay.io, run:
```
$ make deploy
```
Alternately, to deploy the latest release through OLM, run:
```
$ make catalog-deploy
```

### Building and deploying from source:

First set an image repo and tag to use. Make sure that you have permissions to push `file-integrity-operator*` images (and relevant tag) to the repo.
```
$ export IMAGE_REPO=quay.io/myrepo
$ export TAG=mytag
```
With these set, they will apply to the rest of the Makefile targets. Next, build and push the operator and bundle images by running:
```
$ make images && make push
```
Finally, deploy the operator with the built images,
```
$ make deploy
```
or build a catalog and deploy from OLM:
```
$ make catalog && make catalog-deploy
``` 

### FileIntegrity API:

The operator works with `FileIntegrity` objects. Each of these objects represents a managed deployment of AIDE on one or more nodes.

```
apiVersion: fileintegrity.openshift.io/v1alpha1
kind: FileIntegrity
metadata:
  name: example-fileintegrity
  namespace: openshift-file-integrity
spec:
  nodeSelector:
    kubernetes.io/hostname: "ip-10-10-10-1"
  tolerations:
  - key: "myNode"
    operator: "Exists"
    effect: "NoSchedule"
  config:
    name: "myconfig"
    namespace: "openshift-file-integrity"
    key: "config"
    gracePeriod: 20
    maxBackups: 5
  debug: false
status:
  phase: Active
```
In the `spec`:
* **nodeSelector**: Selector for nodes to schedule the scan instances on.
* **tolerations**: Specify tolerations to schedule on nodes with custom taints. When not specified, a default toleration allowing running on master nodes is applied.
* **config**: Point to a ConfigMap containing an AIDE configuration to use instead of the CoreOS optimized default. See "Applying an AIDE config" below.
* **config.gracePeriod**: The number of seconds to pause in between AIDE integrity checks. Frequent AIDE checks on a node may be resource intensive, so it can be useful to specify a longer interval. Defaults to 900 (15 mins).
* **config.maxBackups**: The maximum number of AIDE database and log backups (leftover from the re-init process) to keep on a node. Older backups beyond this number are automatically pruned by the daemon. Defaults to 5.

In the `status`:
* **phase**: The running status of the `FileIntegrity` instance. Can be `Initializing`, `Pending`, or `Active`. `Initializing` is displayed if the FileIntegrity is currently initializing or re-initializing the AIDE database, `Pending` if the FileIntegrity deployment is still being created, and `Active` if the scans are active and ongoing. For node scan results, see the `FileIntegrityNodeStatus` objects explained below.

### Usage:

After deploying the operator, you must create a `FileIntegrity` object. The following example will enable scanning on all nodes.
```
apiVersion: fileintegrity.openshift.io/v1alpha1
kind: FileIntegrity
metadata:
  name: example-fileintegrity
  namespace: openshift-file-integrity
spec:
  config: {}
```

Viewing the scan phase: An `Active` phase indicates that on each node, the AIDE database has been initialized and periodic scanning is enabled:
```
$ oc get fileintegrities -n openshift-file-integrity
NAME                    AGE
example-fileintegrity   11m

$ oc get fileintegrities/example-fileintegrity -n openshift-file-integrity -o jsonpath="{ .status.phase }"
Active
```

Each node will have a corresponding `FileIntegrityNodeStatus` object:
```
$ oc get fileintegritynodestatuses
NAME                                                               AGE
example-fileintegrity-ip-10-0-139-137.us-east-2.compute.internal   4h24m
example-fileintegrity-ip-10-0-140-35.us-east-2.compute.internal    4h24m
example-fileintegrity-ip-10-0-162-216.us-east-2.compute.internal   4h24m
example-fileintegrity-ip-10-0-172-188.us-east-2.compute.internal   4h24m
example-fileintegrity-ip-10-0-210-181.us-east-2.compute.internal   4h24m
example-fileintegrity-ip-10-0-210-89.us-east-2.compute.internal    4h24m
```

The `results` field can contain up to three entries. The most recent Successful scan, the most recent Failed scan (if any), and the most recent Errored scan (if any). When there are multiple entries, the newest `lastProbeTime` indicates the current status.

A Failed scan indicates that there were changes to the files that AIDE monitors, and displays a brief status. The `resultConfigMap` fields point to a ConfigMap containing a more detailed report.

Note: Currently the failure log is only exposed to the admin through this result ConfigMap. In order to provide some permanence of record, the result ConfigMaps are not owned by the FileIntegrity object, so manual cleanup is necessary. Additionally, deleting the FileIntegrity object leaves the AIDE database on the nodes, and the scan state will resume if the FileIntegrity is re-created.

```
$ oc get fileintegritynodestatus/example-fileintegrity-ip-10-0-139-137.us-east-2.compute.internal -o yaml
apiVersion: fileintegrity.openshift.io/v1alpha1
kind: FileIntegrityNodeStatus
...
nodeName: ip-10-0-139-137.us-east-2.compute.internal
results:
- condition: Succeeded
  lastProbeTime: "2020-06-18T01:17:14Z"
- condition: Failed
  filesAdded: 1
  filesChanged: 1
  lastProbeTime: "2020-06-18T01:28:57Z"
  resultConfigMapName: aide-ds-example-fileintegrity-ip-10-0-139-137.us-east-2.compute.internal-failed
  resultConfigMapNamespace: openshift-file-integrity

$ oc get cm/aide-ds-example-fileintegrity-ip-10-0-139-137.us-east-2.compute.internal-failed -n openshift-file-integrity -o jsonpath="{ .data.integritylog }"
AIDE 0.15.1 found differences between database and filesystem!!
Start timestamp: 2020-06-18 02:00:38

Summary:
  Total number of files:        29447
  Added files:                  1
  Removed files:                0
  Changed files:                1


---------------------------------------------------
Added files:
---------------------------------------------------

added: /hostroot/root/.bash_history

---------------------------------------------------
Changed files:
---------------------------------------------------

changed: /hostroot/etc/resolv.conf

---------------------------------------------------
Detailed information about changes:
---------------------------------------------------


File: /hostroot/etc/resolv.conf
 SHA512   : Xl2pzxjmRPtW8bl6Kj49SkKOSBVJgsCI , tebxD8QZd/5/SqsVkExCwVqVO22zxmcq
```
AIDE logs over 1MB are gzip compressed and base64 encoded, due to the configMap data size limit. In this case, you will want to pipe the output of the above command to `base64 -d | gunzip`.  Compressed logs are indicated by the presense of a `file-integrity.openshift.io/compressed` annotation key in the configMap.

### Events

Transitions in the status of the FileIntegrity and FileIntegrityNodeStatus objects are also logged by events. The creation time of the event reflects the latest transition (i.e., Initializing to Active), and not necessarily the latest scan result. However, the newest event will always reflect the most recent status.
```
$ oc get events --field-selector reason=FileIntegrityStatus
LAST SEEN   TYPE     REASON                OBJECT                                MESSAGE
97s         Normal   FileIntegrityStatus   fileintegrity/example-fileintegrity   Pending
67s         Normal   FileIntegrityStatus   fileintegrity/example-fileintegrity   Initializing
37s         Normal   FileIntegrityStatus   fileintegrity/example-fileintegrity   Active
```

When a node has a failed scan, an event is created with the add/changed/removed and configMap information.
```
$ oc get events --field-selector reason=NodeIntegrityStatus
LAST SEEN   TYPE      REASON                OBJECT                                MESSAGE
114m        Normal    NodeIntegrityStatus   fileintegrity/example-fileintegrity   no changes to node ip-10-0-134-173.ec2.internal
114m        Normal    NodeIntegrityStatus   fileintegrity/example-fileintegrity   no changes to node ip-10-0-168-238.ec2.internal
114m        Normal    NodeIntegrityStatus   fileintegrity/example-fileintegrity   no changes to node ip-10-0-169-175.ec2.internal
114m        Normal    NodeIntegrityStatus   fileintegrity/example-fileintegrity   no changes to node ip-10-0-152-92.ec2.internal
114m        Normal    NodeIntegrityStatus   fileintegrity/example-fileintegrity   no changes to node ip-10-0-158-144.ec2.internal
114m        Normal    NodeIntegrityStatus   fileintegrity/example-fileintegrity   no changes to node ip-10-0-131-30.ec2.internal
87m         Warning   NodeIntegrityStatus   fileintegrity/example-fileintegrity   node ip-10-0-152-92.ec2.internal has changed! a:1,c:1,r:0 log:openshift-file-integrity/aide-ds-example-fileintegrity-ip-10-0-152-92.ec2.internal-failed
```

Changes to the number of added/changed/removed files will result in a new event, even if the status of the node has not transitioned.
```
$ oc get events --field-selector reason=NodeIntegrityStatus
LAST SEEN   TYPE      REASON                OBJECT                                MESSAGE
114m        Normal    NodeIntegrityStatus   fileintegrity/example-fileintegrity   no changes to node ip-10-0-134-173.ec2.internal
114m        Normal    NodeIntegrityStatus   fileintegrity/example-fileintegrity   no changes to node ip-10-0-168-238.ec2.internal
114m        Normal    NodeIntegrityStatus   fileintegrity/example-fileintegrity   no changes to node ip-10-0-169-175.ec2.internal
114m        Normal    NodeIntegrityStatus   fileintegrity/example-fileintegrity   no changes to node ip-10-0-152-92.ec2.internal
114m        Normal    NodeIntegrityStatus   fileintegrity/example-fileintegrity   no changes to node ip-10-0-158-144.ec2.internal
114m        Normal    NodeIntegrityStatus   fileintegrity/example-fileintegrity   no changes to node ip-10-0-131-30.ec2.internal
87m         Warning   NodeIntegrityStatus   fileintegrity/example-fileintegrity   node ip-10-0-152-92.ec2.internal has changed! a:1,c:1,r:0 log:openshift-file-integrity/aide-ds-example-fileintegrity-ip-10-0-152-92.ec2.internal-failed
40m         Warning   NodeIntegrityStatus   fileintegrity/example-fileintegrity   node ip-10-0-152-92.ec2.internal has changed! a:3,c:1,r:0 log:openshift-file-integrity/aide-ds-example-fileintegrity-ip-10-0-152-92.ec2.internal-failed
```

### Local testing
```
$ make run
```

### Running the end-to-end suite
```
$ make e2e
```

## Overriding the AIDE configuration
By default the AIDE containers run with an aide.conf that is tailored to a default RHCOS node. If you need to add or exclude files on nodes that are not covered by the default config, you can override it with a modified config.

- Create a ConfigMap containing the aide.conf, e.g.,
```
$ oc project openshift-file-integrity
$ oc create configmap myconf --from-file=aide-conf=aide.conf.rhel8
```
- Post the `FileIntegrity` CR containing the name, namespace, and data key containing the aide.conf in the spec.
```
apiVersion: file-integrity.openshift.io/v1alpha1
kind: FileIntegrity
metadata:
  name: example-fileintegrity
  namespace: openshift-file-integrity
spec:
  config:
    name: myconf
    namespace: openshift-file-integrity
    key: aide-conf
```
* At this point the operator will update the active AIDE config and perform a re-initialization of the AIDE database, as well as a restart of the AIDE pods to begin scanning with the new configuration. A backup of the logs and database from the previously applied configurations are left available on the nodes under /etc/kubernetes.
* The operator automatically converts the `database`, `database_out`, `report_url`, `DBDIR`, and `LOGDIR` options in the configuration to accommodate running inside of a pod.
* Removing the config section from the FileIntegrity resource when active reverts the running config to the default and re-initializes the database.
* In the case of where small modifications are needed (such as excluding a file or directory), it's recommended to copy the [default config](https://github.com/openshift/file-integrity-operator/blob/master/pkg/controller/fileintegrity/config_defaults.go#L16) to a new ConfigMap and add to it as needed.
* Some AIDE configuration options may not be supported by the AIDE container. For example, the `mhash` digest types are not supported. For digest selection, it is recommended to use the default config's [CONTENT_EX group](https://github.com/openshift/file-integrity-operator/blob/master/pkg/controller/fileintegrity/config_defaults.go#L25).
* Manually re-initializing the AIDE database can be done by adding the annotation key `file-integrity.openshift.io/re-init` to the FileIntegrity object.

### Controller metrics

The file-integrity-operator exposes the following FileIntegrity-related metrics to Prometheus when cluster-monitoring is available.

    # HELP file_integrity_operator_phase_total The total number of transitions to the FileIntegrity phase
    # TYPE file_integrity_operator_phase_total counter
    file_integrity_operator_phase_total{phase="Active"} 1
    file_integrity_operator_phase_total{phase="Initializing"} 1
    file_integrity_operator_phase_total{phase="Pending"} 1

    # HELP file_integrity_operator_error_total The total number of FileIntegrity phase errors, per error
    # TYPE file_integrity_operator_error_total counter
    file_integrity_operator_error_total{error="foo"} 1

    # HELP file_integrity_operator_pause_total The total number of FileIntegrity scan pause actions (during node updates)
    # TYPE file_integrity_operator_pause_total counter
    file_integrity_operator_pause_total{node="node-a"} 1

    # HELP file_integrity_operator_unpause_total The total number of FileIntegrity scan unpause actions (during node updates)
    # TYPE file_integrity_operator_unpause_total counter
    file_integrity_operator_unpause_total{node="node-a"} 1

    # HELP file_integrity_operator_reinit_total The total number of FileIntegrity database re-initialization triggers (annotation), per method and node
    # TYPE file_integrity_operator_reinit_total counter
    file_integrity_operator_reinit_total{by="node", node="node-a"} 1
    file_integrity_operator_reinit_total{by="demand", node="node-a"} 1
    file_integrity_operator_reinit_total{by="config", node=""} 1

    # HELP file_integrity_operator_node_status_total The total number of FileIntegrityNodeStatus transitions, per condition and node
    # TYPE file_integrity_operator_node_status_total counter
    file_integrity_operator_node_status_total{condition="Failed",node="node-a"} 1
    file_integrity_operator_node_status_total{condition="Succeeded",node="node-b"} 1
    file_integrity_operator_node_status_total{condition="Errored",node="node-c"} 1

    # HELP file_integrity_operator_node_status_error_total The total number of FileIntegrityNodeStatus errors, per error and node
    # TYPE file_integrity_operator_node_status_error_total counter
    file_integrity_operator_node_status_error_total{error="foo",node="node-a"} 1

    # HELP file_integrity_operator_daemonset_update_total The total number of updates to the FileIntegrity AIDE daemonSet
    # TYPE file_integrity_operator_daemonset_update_total counter
    file_integrity_operator_daemonset_update_total{operation="update"} 1
    file_integrity_operator_daemonset_update_total{operation="delete"} 1
    file_integrity_operator_daemonset_update_total{operation="podkill"} 1

    # HELP file_integrity_operator_reinit_daemonset_update_total The total number of updates to the FileIntegrity re-init signaling daemonSet
    # TYPE file_integrity_operator_reinit_daemonset_update_total counter
    file_integrity_operator_reinit_daemonset_update_total{operation="update"} 1
    file_integrity_operator_reinit_daemonset_update_total{operation="delete"} 1

    # HELP file_integrity_operator_node_failed A gauge that is set to 1 when a node has unresolved integrity failures, and 0 when it is healthy
    # TYPE file_integrity_operator_node_failed gauge
    file_integrity_operator_node_failed{node="node-a"} 1
    file_integrity_operator_node_failed{node="node-b"} 1


After logging into the console, navigating to Monitoring -> Metrics, the file_integrity_operator* metrics can be queried using the metrics dashboard. The `{__name__=~"file_integrity.*"}` query can be used to view the full set of metrics.

Testing for the metrics from the cli can also be done directly with a pod that curls the metrics service. This is useful for troubleshooting.
```
$ oc run --rm -i --restart=Never --image=registry.fedoraproject.org/fedora-minimal:latest -n openshift-file-integrity metrics-test -- bash -c 'curl -ks -H "Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)" https://metrics.openshift-file-integrity.svc:8585/metrics-fio' | grep file
```

## Integrity failure alerts

The operator creates the following default alert (based on the `file_integrity_operator_node_failed` gauge) in the operator namespace that fires when a node has been in a failure state for more than 30 seconds:

```
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: file-integrity
  namespace: openshift-file-integrity
spec:
  groups:
  - name: node-failed
    rules:
    - alert: NodeHasIntegrityFailure
      annotations:
        description: Node {{ $labels.node }} has an an integrity check status of Failed for
          more than 1 second.
        summary: Node {{ $labels.node }} has a file integrity failure
      expr: file_integrity_operator_node_failed{node=~".+"} * on(node) kube_node_info > 0
      for: 1s
      labels:
        severity: warning
```

The severity label and `for` may be adjusted depending on taste.

## Contributor Guide

This guide provides useful information for contributors.

### Proposing Releases

The release process is separated into three phases, with dedicated `make`
targets. All targets require that you supply the `VERSION` prior to
running `make`, which should be a semantic version formatted string (e.g.,
`VERSION=0.1.49`). Additionally, you should ensure that `IMAGE_REPO` and 
`TAG` environment variables are unset before running the targets.

#### Preparing the Release

The first phase of the release process is preparing the release locally. You
can do this by running the `make prepare-release` target. All changes are
staged locally. This is intentional so that you have the opportunity to
review the changes before proposing the release in the next step.

#### Proposing the Release

The second phase of the release is to push the release to a dedicated branch
against the origin repository. You can perform this step using the `make
push-release` target.

Please note, this step makes changes to the upstream repository, so it is
imperative that you review the changes you're committing prior to this step.
This steps also requires that you have necessary permissions on the repository.

#### Releasing Images

The third and final step of the release is to build new images and push them to
an offical image registry. You can build new images and push using `make
release-images`. Note that this operation also requires you have proper
permissions on the remote registry. By default, `make release-images` will push
images to
[Quay](https://quay.io/repository/file-integrity-operator/file-integrity-operator).
You can specify a different repository using the `IMAGE_REPO` environment
variable.
