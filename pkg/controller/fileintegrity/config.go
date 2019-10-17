package fileintegrity

import (
	"errors"
	"strings"
)

// receives an aide.conf and prepares it for use.
// - modifies DBDIR and LOGDIR to be /etc/kubernetes
// - appends /hostroot to any file directives (including ! directives)
// It assumes any file directive in the aide.conf is an absolute path.
func prepareAideConf(in string) (string, error) {
	var conv string
	temp := strings.Split(in, "\n")
	if len(temp) == 0 {
		return "", errors.New("input empty")
	}
	for _, line := range temp {
		if strings.HasPrefix(line, "@@define DBDIR") {
			conv = conv + "@@define DBDIR /hostroot/etc/kubernetes\n"
		} else if strings.HasPrefix(line, "@@define LOGDIR") {
			conv = conv + "@@define LOGDIR /hostroot/etc/kubernetes\n"
		} else if strings.HasPrefix(line, "database=") {
			conv = conv + "database=file:@@{DBDIR}/aide.db.gz\n"
		} else if strings.HasPrefix(line, "database_out=") {
			conv = conv + "database_out=file:@@{DBDIR}/aide.db.gz\n"
		} else if strings.HasPrefix(line, "report_url=file") {
			conv = conv + "report_url=file:@@{LOGDIR}/aide.log\n"
		} else if strings.HasPrefix(line, "/") {
			conv = conv + "/hostroot" + line + "\n"
		} else if strings.HasPrefix(line, "!/") {
			conv = conv + "!/hostroot" + line[1:] + "\n"
		} else {
			if line != "\n" {
				conv = conv + line + "\n"
			}
		}
	}

	return conv[:len(conv)-1], nil
}
