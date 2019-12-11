package fileintegrity

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

var aideScript = `#!/bin/sh
    while true; do
      echo "running AIDE check.."
      aide -c /tmp/aide.conf
      echo "AIDE check returned $?.. sleeping"
      sleep 5m
    done
    exit 1`

var DefaultAideConfig = `@@define DBDIR /hostroot/etc/kubernetes
@@define LOGDIR /hostroot/etc/kubernetes
database=file:@@{DBDIR}/aide.db.gz
database_out=file:@@{DBDIR}/aide.db.gz
gzip_dbout=yes
verbose=5
report_url=file:@@{LOGDIR}/aide.log
report_url=stdout
ALLXTRAHASHES = sha1+rmd160+sha256+sha512+tiger
EVERYTHING = R+ALLXTRAHASHES
NORMAL = p+i+n+u+g+s+m+c+acl+selinux+xattrs+sha512
DIR = p+i+n+u+g+acl+selinux+xattrs
PERMS = p+u+g+acl+selinux+xattrs
LOG = p+u+g+n+S+acl+selinux+xattrs
CONTENT = sha512+ftype
CONTENT_EX = sha512+ftype+p+u+g+n+acl+selinux+xattrs
DATAONLY =  p+n+u+g+s+acl+selinux+xattrs+sha512

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
!/hostroot/etc/docker/certs.d
!/hostroot/etc/selinux/targeted

# Catch everything else in /etc
/hostroot/etc/    CONTENT_EX`
