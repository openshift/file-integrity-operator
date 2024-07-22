package fileintegrity

import (
	"fmt"
	"strings"
	"testing"
)

var testAideFile = `
# comment
@@define DBDIR /blah
@@define LOGDIR /blah
database=file:@@{DBDIR}/something
database_out=file:@@{DBDIR}/aide.db.new.gz
# comment
report_url=file:@@{LOGDIR}/coolaide.log
report_url=stdout
/etc/crowbar
/var/ladder
!/usr/multipass
other
`

var testAidePrepOutput = `
# comment
@@define DBDIR /hostroot/etc/kubernetes
@@define LOGDIR /hostroot/etc/kubernetes
database=file:@@{DBDIR}/aide.db.gz
database_out=file:@@{DBDIR}/aide.db.gz.new
# comment
report_url=file:@@{LOGDIR}/aide.log.new
report_url=stdout
/hostroot/etc/crowbar
/hostroot/var/ladder
!/hostroot/usr/multipass
other
`

var testAideMigrateConfig = `@@define DBDIR /hostroot/etc/kubernetes
@@define LOGDIR /hostroot/etc/kubernetes
database=file:@@{DBDIR}/aide.db.gz
database_out=file:@@{DBDIR}/aide.db.gz.new
gzip_dbout=yes
verbose=5
report_url=file:@@{LOGDIR}/aide.log.new
report_url=stdout
PERMS=p+u+g+acl+selinux+xattrs
CONTENT_EX = sha512+ftype+p+u+g+n+acl+selinux+xattrs
/hostroot/boot/        CONTENT_EX
/hostroot/root/\..* PERMS
/hostroot/root/   CONTENT_EX
!/hostroot/root/\.kube
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

# Catch everything else in /etc
/hostroot/etc/    CONTENT_EX`

var testAideMigrateOutput = `@@define DBDIR /hostroot/etc/kubernetes
@@define LOGDIR /hostroot/etc/kubernetes
database_in=file:@@{DBDIR}/aide.db.gz
database_out=file:@@{DBDIR}/aide.db.gz.new
gzip_dbout=yes
log_level=warning
report_level=changed_attributes
report_url=file:@@{LOGDIR}/aide.log.new
report_url=stdout
PERMS=p+u+g+acl+selinux+xattrs
CONTENTEX=sha512+ftype+p+u+g+n+acl+selinux+xattrs
/hostroot/boot/        CONTENTEX
/hostroot/root/\..* PERMS
/hostroot/root/   CONTENTEX
!/hostroot/root/\.kube
!/hostroot/usr/src/
!/hostroot/usr/tmp/

/hostroot/usr/    CONTENTEX

# OpenShift specific excludes
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

# Catch everything else in /etc
/hostroot/etc/    CONTENTEX`

func TestPrepareAideConf(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "one",
			input:    testAideFile,
			expected: testAidePrepOutput,
		},
	}

	for _, tc := range testCases {
		output, err := prepareAideConf(tc.input)
		if err != nil {
			t.Error(err)
		}
		fmt.Printf("output: \n %s", output)
		if output != tc.expected {
			t.Errorf("expected %s, got %s", tc.expected, output)
		}
	}
}

func TestMigrateAideConf(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "one",
			input:    testAideMigrateConfig,
			expected: testAideMigrateOutput,
		},
	}

	for _, tc := range testCases {
		output, _, err := migrateConfig(tc.input, false)
		if err != nil {
			t.Error(err)
		}
		if output != tc.expected {
			t.Errorf("expected %s, got %s", tc.expected, output)
		}

		// get exact diff of output and expected
		diff := getDiff(output, tc.expected)
		if diff != "" {
			t.Errorf("diff: %s", diff)
		}

	}
}

func getDiff(output string, expected string) string {
	diff := ""
	outputLines := strings.Split(output, "\n")
	expectedLines := strings.Split(expected, "\n")
	// first check if the number of lines is the same
	if len(outputLines) != len(expectedLines) {
		diff += fmt.Sprintf("expected %d lines, got %d lines\n", len(expectedLines), len(outputLines))
	}
	// compare line by line
	for i, line := range expectedLines {
		// print line number and verbose output
		if line != expectedLines[i] {
			diff += fmt.Sprintf("line %d: expected: %s, got: %s\n", i, expectedLines[i], line)
		}
	}

	return diff
}
