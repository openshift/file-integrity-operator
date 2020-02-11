# file-integrity-operator
The file-integrity-operator is a OpenShift Operator that continually runs file integrity checks on the cluster nodes. It deploys a DaemonSet that initializes and runs privileged AIDE ([Advanced Intrusion Detection Environment](https://aide.github.io)) containers on each node, providing a log of files that have been modified since the initial run of the DaemonSet pods.

### Deploying from source:
```
$ (clone repo)
$ oc login -u kubeadmin -p <pw>
$ make image-to-cluster
$ oc create -f deploy/
$ oc create -f deploy/crds
```

### Deploying from OLM:
```
$ (clone repo)
$ oc login -u kubeadmin -p <pw>
$ oc create namespace openshift-file-integrity
$ oc create -f deploy/olm-catalog/operator-source.yaml
```

### Usage:

Viewing the scan phase: An "Active" phase indicates that on each node, the AIDE database has been initialized and periodic scanning is enabled:
```
$ oc get fileintegrities -n openshift-file-integrity
NAME                    AGE
example-fileintegrity   11m

$ oc get fileintegrities/example-fileintegrity -n openshift-file-integrity -o jsonpath="{ .status.phase }"
Active
```

The nodeStatus reports the latest scan results for each node:
```
$ oc get fileintegrities/example-fileintegrity -n openshift-file-integrity -o yaml
apiVersion: file-integrity.openshift.io/v1alpha1
kind: FileIntegrity
metadata:
  creationTimestamp: "2020-02-05T21:06:23Z"
  generation: 1
  name: example-fileintegrity
  namespace: openshift-file-integrity
  resourceVersion: "72304"
  selfLink: /apis/file-integrity.openshift.io/v1alpha1/namespaces/openshift-file-integrity/fileintegrities/example-fileintegrity
  uid: 4de0bc89-b273-4572-ac67-f1c26d70eeff
spec:
  config: {}
status:
  nodeStatus:
  - condition: Succeeded
    lastProbeTime: "2020-02-05T21:08:26Z"
    nodeName: ip-10-0-130-20.ec2.internal
  - condition: Succeeded
    lastProbeTime: "2020-02-05T21:08:28Z"
    nodeName: ip-10-0-154-5.ec2.internal
  - condition: Succeeded
    lastProbeTime: "2020-02-05T21:08:29Z"
    nodeName: ip-10-0-172-210.ec2.internal
  - condition: Succeeded
    lastProbeTime: "2020-02-05T21:08:38Z"
    nodeName: ip-10-0-166-163.ec2.internal
  - condition: Succeeded
    lastProbeTime: "2020-02-05T21:08:39Z"
    nodeName: ip-10-0-143-91.ec2.internal
  - condition: Succeeded
    lastProbeTime: "2020-02-05T21:08:44Z"
    nodeName: ip-10-0-156-193.ec2.internal
  phase: Active
```

If the AIDE check fails on a node, a Failed entry is added to nodeStatus, with the name of a configMap containing the AIDE log.
```
$ oc get fileintegrities/example-fileintegrity -n openshift-file-integrity -o  yaml
apiVersion: file-integrity.openshift.io/v1alpha1
kind: FileIntegrity
metadata:
  creationTimestamp: "2020-02-05T21:06:23Z"
  generation: 1
  name: example-fileintegrity
  namespace: openshift-file-integrity
  resourceVersion: "350107"
  selfLink: /apis/file-integrity.openshift.io/v1alpha1/namespaces/openshift-file-integrity/fileintegrities/example-fileintegrity
  uid: 4de0bc89-b273-4572-ac67-f1c26d70eeff
spec:
  config: {}
status:
  nodeStatus:
  - condition: Succeeded
    lastProbeTime: "2020-02-05T21:08:38Z"
    nodeName: ip-10-0-166-163.ec2.internal
  - condition: Succeeded
    lastProbeTime: "2020-02-05T21:08:39Z"
    nodeName: ip-10-0-143-91.ec2.internal
  - condition: Succeeded
    lastProbeTime: "2020-02-06T12:28:46Z"
    nodeName: ip-10-0-156-193.ec2.internal
  - condition: Failed
    lastProbeTime: "2020-02-06T13:49:06Z"
    nodeName: ip-10-0-143-91.ec2.internal
    resultConfigMapName: aide-ds-ip-10-0-143-91.ec2.internal-failed
    resultConfigMapNamespace: openshift-file-integrity
  - condition: Failed
    lastProbeTime: "2020-02-06T13:50:13Z"
    nodeName: ip-10-0-166-163.ec2.internal
    resultConfigMapName: aide-ds-ip-10-0-166-163.ec2.internal-failed
    resultConfigMapNamespace: openshift-file-integrity
  - condition: Succeeded
    lastProbeTime: "2020-02-06T13:56:29Z"
    nodeName: ip-10-0-130-20.ec2.internal
  - condition: Succeeded
    lastProbeTime: "2020-02-06T13:56:31Z"
    nodeName: ip-10-0-172-210.ec2.internal
  - condition: Succeeded
    lastProbeTime: "2020-02-06T13:56:32Z"
    nodeName: ip-10-0-154-5.ec2.internal
  - condition: Failed
    lastProbeTime: "2020-02-06T13:56:58Z"
    nodeName: ip-10-0-156-193.ec2.internal
    resultConfigMapName: aide-ds-ip-10-0-156-193.ec2.internal-failed
    resultConfigMapNamespace: openshift-file-integrity
  phase: Active 

$ oc get cm/aide-ds-ip-10-0-143-91.ec2.internal-failed -n openshift-file-integrity -o jsonpath="{ .data.integritylog }"
AIDE 0.15.1 found differences between database and filesystem!!
Start timestamp: 2020-02-06 14:00:17

Summary:
  Total number of files:        28455
  Added files:                  0
  Removed files:                0
  Changed files:                2


---------------------------------------------------
Changed files:
---------------------------------------------------

changed: /hostroot/etc/kubernetes/manifests/kube-apiserver-pod.yaml
changed: /hostroot/etc/kubernetes/manifests/kube-controller-manager-pod.yaml

---------------------------------------------------
Detailed information about changes:
---------------------------------------------------


File: /hostroot/etc/kubernetes/manifests/kube-apiserver-pod.yaml
 SHA512   : 1ommsBCFpCYbgbks6NDDOc6jdscCwpAy , v1vmOX0S7M59LCVi8vfW8fP0BsQl14k+

File: /hostroot/etc/kubernetes/manifests/kube-controller-manager-pod.yaml
 SHA512   : +yS3z7KOFSPNT+nIRMWXkry4qM4swwDG , OJgRKucyDdAMlPzloWetrn3cEO7mfM94
```

### Local testing
```
$ make run
```

### Running the end-to-end suite
```
$ make e2e
```

## Applying an AIDE config
It's possible to provide the file-integrity-operator with an existing aide.conf. The provided aide.conf will be automatically converted to run in a pod, so there is no need to adjust the database and file directives to accommodate the operator.

- Create a configMap containing the aide.conf, e.g.,
```
$ oc project openshift-file-integrity
$ oc create configmap myconf --from-file=aide-conf=aide.conf.rhel8
```
- Post the FileIntegrity CR containing the name, namespace, and data key containing the aide.conf in the spec.
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
