package fileintegrity

const aideLogPath = "/hostroot/etc/kubernetes/aide.log"

var aideInitContainerScript = `#!/bin/sh
    if test ! -f /hostroot/etc/kubernetes/aide.db.gz; then
      echo "initializing AIDE db"
      aide -c /tmp/aide.conf -i
    fi
    if test -f /hostroot/etc/kubernetes/aide.reinit; then
      echo "reinitializing AIDE db"
      mv /hostroot/etc/kubernetes/aide.db.gz /hostroot/etc/kubernetes/aide.db.gz.backup-$(date +%s)
      mv /hostroot/etc/kubernetes/aide.log /hostroot/etc/kubernetes/aide.log.backup-$(date +%s)
      aide -c /tmp/aide.conf -i
      rm -f /hostroot/etc/kubernetes/aide.reinit
    fi
`

var aideReinitContainerScript = `#!/bin/sh
    touch /hostroot/etc/kubernetes/aide.reinit
`

// An AIDE run is executed every 10s and the output is set in the
// /hostroot/etc/kubernetes/aide.latest-result.log file.
// If the file /hostroot/etc/kubernetes/holdoff is found, the check
// is skipped
// TODO: Make time configurable
var aideScript = `#!/bin/sh
    while true; do
      if [ -f /hostroot/etc/kubernetes/holdoff ]; then
        continue
      fi
      echo "running AIDE check.."
      aide -c /tmp/aide.conf
      result=$?
      echo "$result" > /hostroot/etc/kubernetes/aide.latest-result.log
      echo "AIDE check returned $result.. sleeping"
      sleep 10s
    done
    exit 1`

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

# Catch everything else in /etc
/hostroot/etc/    CONTENT_EX`
