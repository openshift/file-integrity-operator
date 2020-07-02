package fileintegrity

const aideLogPath = "/hostroot/etc/kubernetes/aide.log"

var aideReinitContainerScript = `#!/bin/sh
    touch /hostroot/etc/kubernetes/aide.reinit
`

var aidePauseContainerScript = `#!/bin/sh
	sleep infinity & PID=$!
	trap "kill $PID" INT TERM
	wait $PID || true
`

// NOTE: Needs to be in sync with `testAideConfig` in test/e2e/helpers.go, except for the heading comment.
var DefaultAideConfig = `@@define DBDIR /hostroot/etc/kubernetes
@@define LOGDIR /hostroot/etc/kubernetes
database=file:@@{DBDIR}/aide.db.gz
database_out=file:@@{DBDIR}/aide.db.gz
gzip_dbout=yes
verbose=5
report_url=file:@@{LOGDIR}/aide.log
report_url=stdout
PERMS = p+u+g+acl+selinux+xattrs
CONTENT_EX = sha512+ftype+p+u+g+n+acl+selinux+xattrs

/hostroot/boot/        CONTENT_EX
/hostroot/root/\..* PERMS
/hostroot/root/   CONTENT_EX
!/hostroot/usr/src/
!/hostroot/usr/tmp/

/hostroot/usr/    CONTENT_EX

# OpenShift specific excludes
!/hostroot/opt/
!/hostroot/var
!/hostroot/etc/NetworkManager/system-connections/
!/hostroot/etc/mtab$
!/hostroot/etc/.*~
!/hostroot/etc/kubernetes/static-pod-resources
!/hostroot/etc/kubernetes/aide.*
!/hostroot/etc/kubernetes/manifests
!/hostroot/etc/docker/certs.d
!/hostroot/etc/selinux/targeted
!/hostroot/etc/openvswitch/conf.db

# Catch everything else in /etc
/hostroot/etc/    CONTENT_EX`
