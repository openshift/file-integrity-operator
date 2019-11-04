# file-integrity-operator
The file-integrity-operator is a OpenShift/Kubernetes Operator that continually runs file integrity checks on the cluster nodes. It deploys a DaemonSet that initializes and runs privileged AIDE ([Advanced Intrusion Detection Environment](https://aide.github.io)) containers on each node, providing a log of files that have been modified since the initial run of the DaemonSet pods.

This repo is a POC for host file integrity monitoring that is a work-in-progress.

### Deploying:
```
$ (clone repo)
$ oc create -f deploy/ns.yaml
$ oc create -f deploy/
$ oc create -f deploy/crds

$ oc get all -n openshift-file-integrity
NAME                                           READY   STATUS    RESTARTS   AGE
pod/aiderunner-ccfbh                           1/1     Running   9          4h30m
pod/aiderunner-jb2q8                           1/1     Running   9          4h30m
pod/aiderunner-l2f7v                           1/1     Running   9          4h30m
pod/file-integrity-operator-65c8b579f5-vsstn   1/1     Running   0          4h31m

NAME                                      TYPE        CLUSTER-IP       EXTERNAL-IP   PORT(S)             AGE
service/file-integrity-operator-metrics   ClusterIP   172.30.114.139   <none>        8383/TCP,8686/TCP   4h52m

NAME                        DESIRED   CURRENT   READY   UP-TO-DATE   AVAILABLE   NODE SELECTOR   AGE
daemonset.apps/aiderunner   3         3         3       3            3           <none>          4h30m

NAME                                      READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/file-integrity-operator   1/1     1            1           4h53m

NAME                                                 DESIRED   CURRENT   READY   AGE
replicaset.apps/file-integrity-operator-65c8b579f5   1         1         1       4h53m
```

Viewing the AIDE log
```
$ oc logs -n openshift-file-integrity pod/aiderunner-ccfbh
....
Summary:
  Total number of files:        30519
  Added files:                  6
  Removed files:                2
  Changed files:                1


---------------------------------------------------
Added files:
---------------------------------------------------

added: /hostroot/var/log/containers/aide-59cg9_foo_aide-252e84cc4108c2476644f075d863ec046b9ebc47efb57fe829c09706dad5c98d.log
added: /hostroot/var/log/containers/aiderunner-ccfbh_openshift-file-integrity_aide-38e4c424c6abdb7b89370164a71847daa94aad36a08248ac63dae562e4fb1c4e.log
added: /hostroot/var/log/containers/aiderunner-ccfbh_openshift-file-integrity_aide-3a21bfdb289764b23765aae7658493f6247491a00a9d14551cb0226311f53bfb.log
added: /hostroot/var/log/journal/25731cc7fea54646bf64d369b5008032/system@bc4cdafcf78343c2addc2e6dbde4fa71-000000000001ec91-000592b376af4e05.journal
added: /hostroot/var/log/journal/25731cc7fea54646bf64d369b5008032/system@bc4cdafcf78343c2addc2e6dbde4fa71-000000000003db0a-000592b9c1ea6a41.journal
added: /hostroot/var/log/journal/25731cc7fea54646bf64d369b5008032/system@bc4cdafcf78343c2addc2e6dbde4fa71-000000000005c927-000592bfe964d6e2.journal

---------------------------------------------------
Removed files:
---------------------------------------------------

removed: /hostroot/var/log/containers/aide-xskxw_foo_aide-162eb356e39d39b24f7431dadac8916d76e257265627693e949c01a6ea883bc4.log
removed: /hostroot/var/log/containers/prometheus-adapter-8578f46566-9nw29_openshift-monitoring_prometheus-adapter-98b63f2426615f07da862d802555437f27a70a537b1d90c4eb530579b12f111c.log

---------------------------------------------------
Changed files:
---------------------------------------------------

changed: /hostroot/var/log/journal/25731cc7fea54646bf64d369b5008032/system.journal

---------------------------------------------------
Detailed information about changes:
---------------------------------------------------


File: /hostroot/var/log/journal/25731cc7fea54646bf64d369b5008032/system.journal
 Size     : 117440512                        , 25165824
 Inode    : 25220101                         , 25799166
 XAttrs   : old = num=1
             [1] user.crtime_usec <=> AFSvdrOSBQA=
            new = num=1
             [1] user.crtime_usec <=> XorIGMaSBQA=
AIDE check returned 7.. sleeping
```
The AIDE logs are also available on the host filesystem at /etc/kubernetes/aide.log.
### Building
This repo was established using the operator-sdk. Making changes and rebuilding calls for the following commands:
```
$ operator-sdk build docker.io/mrogers950/file-integrity-operator
$ docker push docker.io/mrogers950/file-integrity-operator:latest
``` 
When forking the repo and making your own changes you should adjust the docker image repo name and paths to push to your own repo, and then change deploy/operator.yaml to refer to your image.
The operand AIDE container is located at `docker.io/mrogers950/aide`.

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

## TODO
- Fine-tune the AIDE rules in the aide.conf ConfigMap. The above deployment example shows the rules cover the host's /var/log/ for attributes and added/removed files, which should be corrected.