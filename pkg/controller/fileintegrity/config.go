package fileintegrity

import (
	"bufio"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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
			conv = conv + "database_out=file:@@{DBDIR}/aide.db.gz.new\n"
		} else if strings.HasPrefix(line, "report_url=file") {
			conv = conv + "report_url=file:@@{LOGDIR}/aide.log.new\n"
		} else if strings.HasPrefix(line, "/") {
			if strings.HasPrefix(line, "/hostroot") {
				// line already has /hostroot, skip prepending it
				conv = conv + line + "\n"
			} else {
				conv = conv + "/hostroot" + line + "\n"
			}
		} else if strings.HasPrefix(line, "!/") {
			if strings.HasPrefix(line, "!/hostroot") {
				// line already has !/hostroot, skip prepending it
				conv = conv + line + "\n"
			} else {
				conv = conv + "!/hostroot" + line[1:] + "\n"
			}
		} else {
			if line != "\n" {
				conv = conv + line + "\n"
			}
		}
	}

	return conv[:len(conv)-1], nil
}

// Check https://github.com/aide/aide/blob/master/ChangeLog for deprecations and changes
// in the configuration file
func migrateConfig(config string, ignore bool) (outputConfig string, ignoredLines []string, err error) {
	scanner := bufio.NewScanner(strings.NewReader(config))
	var newConfig strings.Builder
	var tgroupRegex = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*(.*)`)

	replaceableKeys := map[string]string{
		"verbose":           "log_level", // this is a special case and will be handled separately, see convertVerboseToLogReportLevels
		"database":          "database_in",
		"grouped":           "report_grouped",
		"summarize_changes": "report_summarize_changes",
		"report_attributes": "report_force_attrs",
		"CONTENT_EX":        "CONTENTEX",
		"@@ifdef":           "@@if defined",
		"@@ifndef":          "@@if not defined",
		"@@ifhost":          "@@if hostname",
		"@@ifnhost":         "@@if not hostname",
	}

	nonReplaceableKeys := map[string]string{
		"report_ignore_added_attrs":   "",
		"report_ignore_removed_attrs": "",
		"report_ignore_changed_attrs": "",
		"report_ignore_e2fsattrs":     "",
	}

	for scanner.Scan() {
		line := scanner.Text()
		// trim leading and trailing spaces
		line = strings.TrimSpace(line)

		// Skip comments
		if strings.HasPrefix(line, "#") {
			newConfig.WriteString(line + "\n")
			continue
		}
		// Skip empty lines
		if len(line) == 0 {
			newConfig.WriteString(line + "\n")
			continue
		}

		matches := tgroupRegex.FindStringSubmatch(line)
		if len(matches) != 3 { // find a key value pair in the line
			willContinue := false
			for key, value := range replaceableKeys {
				if strings.HasPrefix(line, key) {
					line = strings.Replace(line, key, value, 1)
					newConfig.WriteString(line + "\n")
					// break out of the loop and continue to the next parent loop as well
					willContinue = true
					break
				}
			}

			// special case for CONTENT_EX
			if strings.HasSuffix(line, "CONTENT_EX") {
				newConfig.WriteString(strings.Replace(line, "CONTENT_EX", "CONTENTEX", 1) + "\n")
				continue
			}
			if willContinue {
				continue
			}
			newConfig.WriteString(line + "\n")
		} else {
			key, value := matches[1], matches[2]
			if newKey, ok := replaceableKeys[key]; ok {
				if key == "verbose" {
					logLevel, reportLevel := convertVerboseToLogReportLevels(value)
					if logLevel != "" {
						newConfig.WriteString(newKey + "=" + logLevel + "\n")
					}
					if reportLevel != "" {
						newConfig.WriteString("report_level=" + reportLevel + "\n")
					}
					if logLevel == "" && reportLevel == "" {
						// if both are empty, it means the verbose value is invalid
						return "", ignoredLines, fmt.Errorf("Invalid verbose value: %s", value)
					}
				} else {
					newConfig.WriteString(newKey + "=" + value + "\n")
				}
			} else if _, ok := nonReplaceableKeys[key]; ok {
				if !ignore {
					return "", ignoredLines, fmt.Errorf("Deprecated option found: %s, please use %s separately", line, nonReplaceableKeys[key])
				} else {
					ignoredLines = append(ignoredLines, line)
					newConfig.WriteString(line + "\n")
				}
			} else {
				newConfig.WriteString(line + "\n")
			}

		}
	}
	if err := scanner.Err(); err != nil {
		return "", ignoredLines, err
	}

	return strings.TrimSuffix(newConfig.String(), "\n"), ignoredLines, nil
}

func convertVerboseToLogReportLevels(verboseValue string) (string, string) {
	v, err := strconv.Atoi(verboseValue)
	if err != nil {
		return "", ""
	}

	var logLevel, reportLevel string
	switch {
	case v == 0:
		logLevel = "error"
		reportLevel = "summary"
	case v >= 1 && v <= 5:
		logLevel = "warning"
		switch v {
		case 1:
			reportLevel = "summary"
		case 2, 3, 4:
			reportLevel = "list_entries"
		case 5:
			reportLevel = "changed_attributes"
		}
	case v >= 6 && v <= 10:
		logLevel = "notice"
		if v == 6 {
			reportLevel = "added_removed_attributes"
		} else {
			reportLevel = "added_removed_entries"
		}
	case v >= 11 && v <= 20:
		logLevel = "info"
		reportLevel = "added_removed_entries"
	case v > 20 && v <= 200:
		logLevel = "config"
		reportLevel = "added_removed_entries"
	case v >= 199 && v <= 220:
		logLevel = "debug"
		reportLevel = "added_removed_entries"
	case v >= 221 && v <= 255:
		logLevel = "trace"
		reportLevel = "added_removed_entries"
	}

	return logLevel, reportLevel
}
