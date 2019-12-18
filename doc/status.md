## Integrity Scan Status Design

In addition to launching AIDE on the nodes, the FIO needs to convey the scan status to the administrator, and allow
them to view the log of failed scan items without logging into the node.

The administrator workflow would be:
1. View the status field of the FileIntegrity CR
2. Notice a failed scan on one node. The failed entry points to a configMap.
3. View the configMap.

```
...
status:
  phase: Active
  nodeStatus:
  - nodeName: foo
    lastProbeTime: <timestamp>
    condition: Succeeded
  - nodeName: foo2
    lastProbeTime: <timestamp>
    condition: Failed
    resultConfigMapName: foo2-aide-results-asdf
    resultConfigMapNamespace: openshift-file-integrity
...
```
* The FileIntegrity controller (pkg/controller/fileintegrity/fileintegrity_controller.go) deploys a daemonSet that runs
  a `logcollector` container (a generic variant of the `scapresults` container). This container runs as a daemon on each
  node that periodically places /etc/kubernetes/aide.log into a configMap. The configMaps are labeled in a way that they
  can be identified easily by another controller and indicate which node the log was obtained from.
* The configmap controller (pkg/controller/configmap/configmap_controller.go) processes the configMaps created by the
  `logcollector` daemons (the configMap controller also processes the AIDE configuration, but the following happens in a
  different code branch in the controller).
    * For each configMap, the AIDE log is parsed for the latest scan results. If the scan resulted in differences
      between the database and filesystem, the log is copied to a new, permanent configMap. After processing (regardless
      of scan results) the temporary configMaps are deleted and the status.nodeStatus entries are updated.
    * The nodeStatus field gets an entry for each node with the latest status. For a condition: Failed entry,
      resultConfigMapName and resultConfigMapNamespace are included with the name and namespace of the permanent
      configMap.

TODOs:
* Create a Kubernetes Event when the integrity scan on a node has failed.
* Use some kind of object storage for logs instead of configMaps.
