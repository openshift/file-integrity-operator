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

var defaultAideConfig = `@@define DBDIR /hostroot/etc/kubernetes
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
/hostroot/opt/        CONTENT
/hostroot/root/\..* PERMS
/hostroot/root/   CONTENT_EX
!/hostroot/usr/src/
!/hostroot/usr/tmp/

/hostroot/usr/    CONTENT_EX

# trusted databases
/hostroot/etc/hosts$      CONTENT_EX
/hostroot/etc/host.conf$  CONTENT_EX
/hostroot/etc/hostname$   CONTENT_EX
/hostroot/etc/issue$      CONTENT_EX
/hostroot/etc/issue.net$  CONTENT_EX
/hostroot/etc/protocols$  CONTENT_EX
/hostroot/etc/services$   CONTENT_EX
/hostroot/etc/localtime$  CONTENT_EX
/hostroot/etc/alternatives/ CONTENT_EX
/hostroot/etc/sysconfig   CONTENT_EX
/hostroot/etc/mime.types$ CONTENT_EX
/hostroot/etc/terminfo/   CONTENT_EX
/hostroot/etc/exports$    CONTENT_EX
/hostroot/etc/fstab$      CONTENT_EX
/hostroot/etc/passwd$     CONTENT_EX
/hostroot/etc/group$      CONTENT_EX
/hostroot/etc/gshadow$    CONTENT_EX
/hostroot/etc/shadow$     CONTENT_EX
/hostroot/etc/subgid$     CONTENT_EX
/hostroot/etc/subuid$     CONTENT_EX
/hostroot/etc/security/opasswd$ CONTENT_EX
/hostroot/etc/skel/       CONTENT_EX
/hostroot/etc/subuid$     CONTENT_EX
/hostroot/etc/subgid$     CONTENT_EX
/hostroot/etc/sssd/       CONTENT_EX
/hostroot/etc/machine-id$ CONTENT_EX
/hostroot/etc/system-release-cpe$ CONTENT_EX
/hostroot/etc/shells$     CONTENT_EX
/hostroot/etc/tmux.conf$  CONTENT_EX
/hostroot/etc/xattr.conf$ CONTENT_EX

# networking
/hostroot/etc/hosts.allow$   CONTENT_EX
/hostroot/etc/hosts.deny$    CONTENT_EX
!/hostroot/etc/NetworkManager/system-connections/
/hostroot/etc/NetworkManager/ CONTENT_EX
/hostroot/etc/networks$ CONTENT_EX
/hostroot/etc/dhcp/ CONTENT_EX
/hostroot/etc/resolv.conf$ DATAONLY
/hostroot/etc/nscd.conf$ CONTENT_EX

# logins and accounts
/hostroot/etc/login.defs$ CONTENT_EX
/hostroot/etc/libuser.conf$ CONTENT_EX
/hostroot/etc/pam.d/ CONTENT_EX
/hostroot/etc/security/ CONTENT_EX
/hostroot/etc/securetty$ CONTENT_EX
/hostroot/etc/polkit-1/ CONTENT_EX
/hostroot/etc/sudo.conf$ CONTENT_EX
/hostroot/etc/sudoers CONTENT_EX
/hostroot/etc/sudoers.d/ CONTENT_EX

# Shell/X startup files
/hostroot/etc/profile$ CONTENT_EX
/hostroot/etc/profile.d/ CONTENT_EX
/hostroot/etc/bashrc$ CONTENT_EX
/hostroot/etc/bash_completion.d/ CONTENT_EX
/hostroot/etc/zprofile$ CONTENT_EX
/hostroot/etc/zshrc$ CONTENT_EX
/hostroot/etc/zlogin$ CONTENT_EX
/hostroot/etc/zlogout$ CONTENT_EX

# Pkg manager
/hostroot/etc/dnf/ CONTENT_EX
/hostroot/etc/yum.conf$ CONTENT_EX

# auditing
# AIDE produces an audit record, so this becomes perpetual motion.
/hostroot/etc/audit/ CONTENT_EX
/hostroot/etc/libaudit.conf$ CONTENT_EX
/hostroot/etc/aide.conf$  CONTENT_EX

# System logs
/hostroot/etc/rsyslog.conf$ CONTENT_EX
/hostroot/etc/logrotate.conf$ CONTENT_EX
/hostroot/etc/logrotate.d/ CONTENT_EX
/hostroot/etc/systemd/journald.conf$ CONTENT_EX

# secrets
/hostroot/etc/pkcs11/ CONTENT_EX
/hostroot/etc/pki/ CONTENT_EX
/hostroot/etc/crypto-policies/ CONTENT_EX

# init system
/hostroot/etc/systemd/ CONTENT_EX
/hostroot/etc/rc.d/ CONTENT_EX
/hostroot/etc/tmpfiles.d/ CONTENT_EX

# boot config
/hostroot/etc/default/ CONTENT_EX
/hostroot/etc/grub.d/ CONTENT_EX
/hostroot/etc/dracut.conf CONTENT_EX
/hostroot/etc/dracut.conf.d/ CONTENT_EX

# glibc linker
/hostroot/etc/ld.so.cache$ CONTENT_EX
/hostroot/etc/ld.so.conf$ CONTENT_EX
/hostroot/etc/ld.so.conf.d/ CONTENT_EX

# kernel config
/hostroot/etc/sysctl.conf CONTENT_EX
/hostroot/etc/sysctl.d/ CONTENT_EX
/hostroot/etc/modprobe.d/ CONTENT_EX
/hostroot/etc/modules-load.d/ CONTENT_EX
/hostroot/etc/depmod.d/ CONTENT_EX
/hostroot/etc/udev/ CONTENT_EX
/hostroot/etc/crypttab$ CONTENT_EX

#### Daemons ####
# time keeping
/hostroot/etc/chrony.conf CONTENT_EX
/hostroot/etc/chrony.keys$ CONTENT_EX

# mail
/hostroot/etc/aliases$ CONTENT_EX
/hostroot/etc/aliases.db$ CONTENT_EX

# ssh
/hostroot/etc/ssh/sshd_config CONTENT_EX
/hostroot/etc/ssh/ssh_config CONTENT_EX

# xinetd
/hostroot/etc/xinetd.conf$ CONTENT_EX
/hostroot/etc/xinetd.d/ CONTENT_EX

# Ignore some files
!/hostroot/etc/mtab$
!/hostroot/etc/.*~

# Now everything else
/hostroot/etc/    PERMS

# With AIDE's default verbosity level of 5, these would give lots of
# warnings upon tree traversal. It might change with future version.
#
#=/lost\+found    DIR
#=/home           DIR

# Admins dot files constantly change, just check perms
/hostroot/root/\..* PERMS
!/hostroot/root/.xauth*

# OpenShift specific
!/hostroot/var
!/hostroot/etc/kubernetes/static-pod-resources
!/hostroot/etc/kubernetes/aide.reinit`
