package fileintegrity

import (
	"fmt"
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
database_out=file:@@{DBDIR}/aide.db.gz
# comment
report_url=file:@@{LOGDIR}/aide.log
report_url=stdout
/hostroot/etc/crowbar
/hostroot/var/ladder
!/hostroot/usr/multipass
other
`

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
