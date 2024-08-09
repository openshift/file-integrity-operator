package fileintegrity

import "os"

const aideLogPath = "/hostroot/etc/kubernetes/aide.log"

var aideReinitContainerScript = `#!/bin/sh
    touch /hostroot/run/aide.reinit
`

var aidePauseContainerScript = `#!/bin/sh
	sleep infinity & PID=$!
	trap "kill $PID" INT TERM
	wait $PID || true
`

var DefaultAideConfigCommonStart018 = `@@define DBDIR /hostroot/etc/kubernetes
@@define LOGDIR /hostroot/etc/kubernetes
database_in=file:@@{DBDIR}/aide.db.gz
database_out=file:@@{DBDIR}/aide.db.gz.new
gzip_dbout=yes
log_level=warning
report_level=changed_attributes
report_url=file:@@{LOGDIR}/aide.log.new
report_url=stdout
PERMS = p+u+g+acl+selinux+xattrs
CONTENTEX = sha512+ftype+p+u+g+n+acl+selinux+xattrs

/hostroot/boot/        CONTENTEX
/hostroot/root/\..* PERMS
/hostroot/root/   CONTENTEX
!/hostroot/root/\.kube
!/hostroot/usr/src/
!/hostroot/usr/tmp/
`

var DefaultAideConfigCommonEnd018 = `# Catch everything else in /etc
/hostroot/etc/    CONTENTEX`

var DefaultAideConfigCommonStart = `@@define DBDIR /hostroot/etc/kubernetes
@@define LOGDIR /hostroot/etc/kubernetes
database=file:@@{DBDIR}/aide.db.gz
database_out=file:@@{DBDIR}/aide.db.gz.new
gzip_dbout=yes
verbose=5
report_url=file:@@{LOGDIR}/aide.log.new
report_url=stdout
PERMS = p+u+g+acl+selinux+xattrs
CONTENT_EX = sha512+ftype+p+u+g+n+acl+selinux+xattrs

/hostroot/boot/        CONTENT_EX
/hostroot/root/\..* PERMS
/hostroot/root/   CONTENT_EX
!/hostroot/root/\.kube
!/hostroot/usr/src/
!/hostroot/usr/tmp/

/hostroot/usr/    CONTENT_EX
`

var DefaultAideConfigCommonEnd = `# Catch everything else in /etc
/hostroot/etc/    CONTENT_EX`

// NOTE: Needs to be in sync with `testAideConfig` in test/e2e/helpers.go, except for the heading comment.
var DefaultAideConfigExclude = `# OpenShift specific excludes
!/hostroot/opt/
!/hostroot/var
!/hostroot/etc/NetworkManager/system-connections/
!/hostroot/etc/mtab$
!/hostroot/etc/.*~
!/hostroot/etc/kubernetes/static-pod-resources
!/hostroot/etc/kubernetes/aide.*
!/hostroot/etc/kubernetes/manifests
!/hostroot/etc/kubernetes/kubelet-ca.crt
!/hostroot/etc/docker/certs.d
!/hostroot/etc/selinux/targeted
!/hostroot/etc/openvswitch/conf.db
!/hostroot/etc/kubernetes/cni/net.d
!/hostroot/etc/kubernetes/cni/net.d/*
!/hostroot/etc/machine-config-daemon/currentconfig$
!/hostroot/etc/machine-config-daemon/node-annotation.json*
!/hostroot/etc/pki/ca-trust/extracted/java/cacerts$
!/hostroot/etc/cvo/updatepayloads
!/hostroot/etc/cni/multus/certs
!/hostroot/etc/kubernetes/compliance-operator
!/hostroot/etc/kubernetes/node-feature-discovery
!/hostroot/etc/mco/internal-registry-pull-secret.json$
`

func GetAideConfigDefault() string {
	if os.Getenv("AIDE_VERSION") == "0.18" {
		return DefaultAideConfigCommonStart018 + DefaultAideConfigExclude + DefaultAideConfigCommonEnd018
	}
	return DefaultAideConfigCommonStart + DefaultAideConfigExclude + DefaultAideConfigCommonEnd
}
