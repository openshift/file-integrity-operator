package fileintegrity

var aideScript = `#!/bin/sh
    if test ! -f /hostroot/etc/kubernetes/aide.db.gz; then
      echo "initializing AIDE db"
      aide -c /tmp/aide.conf -i
    fi
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
    verbose=10
    report_url=file:@@{LOGDIR}/aide.log
    report_url=stdout
    # These are the default rules.
    FIPSR = p+i+n+u+g+s+m+c+acl+selinux+xattrs+sha256
    ALLXTRAHASHES = sha1+rmd160+sha256+sha512+tiger
    EVERYTHING = R+ALLXTRAHASHES
    NORMAL = FIPSR+sha512
    DIR = p+i+n+u+g+acl+selinux+xattrs
    PERMS = p+i+u+g+acl+selinux
    LOG = >
    LSPP = FIPSR+sha512
    DATAONLY =  p+n+u+g+s+acl+selinux+xattrs+sha256
    /hostroot/boot   NORMAL
    /hostroot/bin    NORMAL
    /hostroot/sbin   NORMAL
    /hostroot/lib    NORMAL
    /hostroot/lib64  NORMAL
    /hostroot/opt    NORMAL
    /hostroot/usr    NORMAL
    /hostroot/root   NORMAL
    !/hostroot/usr/src
    !/hostroot/usr/tmp
    /hostroot/etc    PERMS
    !/hostroot/etc/mtab
    !/hostroot/etc/.*~
    /hostroot/etc/exports  NORMAL
    /hostroot/etc/fstab    NORMAL
    /hostroot/etc/passwd   NORMAL
    /hostroot/etc/group    NORMAL
    /hostroot/etc/gshadow  NORMAL
    /hostroot/etc/shadow   NORMAL
    /hostroot/etc/security/opasswd   NORMAL
    /hostroot/etc/hosts.allow   NORMAL
    /hostroot/etc/hosts.deny    NORMAL
    /hostroot/etc/sudoers NORMAL
    /hostroot/etc/skel NORMAL
    /hostroot/etc/logrotate.d NORMAL
    /hostroot/etc/resolv.conf DATAONLY
    /hostroot/etc/nscd.conf NORMAL
    /hostroot/etc/securetty NORMAL
    /hostroot/etc/profile NORMAL
    /hostroot/etc/bashrc NORMAL
    /hostroot/etc/bash_completion.d/ NORMAL
    /hostroot/etc/login.defs NORMAL
    /hostroot/etc/zprofile NORMAL
    /hostroot/etc/zshrc NORMAL
    /hostroot/etc/zlogin NORMAL
    /hostroot/etc/zlogout NORMAL
    /hostroot/etc/profile.d/ NORMAL
    /hostroot/etc/X11/ NORMAL
    /hostroot/etc/yum.conf NORMAL
    /hostroot/etc/yumex.conf NORMAL
    /hostroot/etc/yumex.profiles.conf NORMAL
    /hostroot/etc/yum/ NORMAL
    /hostroot/etc/yum.repos.d/ NORMAL
    /hostroot/var/log   LOG
    /hostroot/var/run/utmp LOG
    !/hostroot/var/log/sa
    !/hostroot/var/log/pods
    !/hostroot/var/log/aide.log
    /hostroot/etc/audit/ LSPP
    /hostroot/etc/libaudit.conf LSPP
    /hostroot/usr/sbin/stunnel LSPP
    /hostroot/var/spool/at LSPP
    /hostroot/etc/at.allow LSPP
    /hostroot/etc/at.deny LSPP
    /hostroot/etc/cron.allow LSPP
    /hostroot/etc/cron.deny LSPP
    /hostroot/etc/cron.d/ LSPP
    /hostroot/etc/cron.daily/ LSPP
    /hostroot/etc/cron.hourly/ LSPP
    /hostroot/etc/cron.monthly/ LSPP
    /hostroot/etc/cron.weekly/ LSPP
    /hostroot/etc/crontab LSPP
    /hostroot/var/spool/cron/root LSPP
    /hostroot/etc/login.defs LSPP
    /hostroot/etc/securetty LSPP
    /hostroot/var/log/faillog LSPP
    /hostroot/var/log/lastlog LSPP
    /hostroot/etc/hosts LSPP
    /hostroot/etc/sysconfig LSPP
    /hostroot/etc/inittab LSPP
    /hostroot/etc/grub/ LSPP
    /hostroot/etc/rc.d LSPP
    /hostroot/etc/ld.so.conf LSPP
    /hostroot/etc/localtime LSPP
    /hostroot/etc/sysctl.conf LSPP
    /hostroot/etc/modprobe.conf LSPP
    /hostroot/etc/pam.d LSPP
    /hostroot/etc/security LSPP
    /hostroot/etc/aliases LSPP
    /hostroot/etc/postfix LSPP
    /hostroot/etc/ssh/sshd_config LSPP
    /hostroot/etc/ssh/ssh_config LSPP
    /hostroot/etc/stunnel LSPP
    /hostroot/etc/vsftpd.ftpusers LSPP
    /hostroot/etc/vsftpd LSPP
    /hostroot/etc/issue LSPP
    /hostroot/etc/issue.net LSPP
    /hostroot/etc/cups LSPP
    !/hostroot/var/log/and-httpd
    /hostroot/root/\..* PERMS`
