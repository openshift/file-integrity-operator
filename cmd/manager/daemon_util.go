/*
Copyright © 2020 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package manager

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	backoff "github.com/cenkalti/backoff/v4"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/api/events/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/file-integrity-operator/pkg/common"
)

func aideReadDBPath(c *daemonConfig) string {
	return path.Join(c.FileDir, aideReadDBFileName)
}

func aideWriteDBPath(c *daemonConfig) string {
	return path.Join(c.FileDir, aideWritingDBFileName)
}

func aideReadLogPath(c *daemonConfig) string {
	return path.Join(c.FileDir, aideReadLogFileName)
}

func aideWriteLogPath(c *daemonConfig) string {
	return path.Join(c.FileDir, aideWritingLogFileName)
}

func aideReinitPath(c *daemonConfig) string {
	return path.Join(c.RunDir, aideReinitFileName)
}

// We still care about this for upgrades, so it can be cleaned up.
func legacyAideReinitPath(c *daemonConfig) string {
	return path.Join(c.FileDir, aideReinitFileName)
}

func aideConfigPath(c *daemonConfig) string {
	return path.Join(c.ConfigDir, aideConfigFileName)
}

func logAndTryReportingDaemonError(ctx context.Context, rt *daemonRuntime, conf *daemonConfig, fmt string, err error) {
	LOG(fmt, err)
	if reportErr := reportDaemonError(ctx, rt, conf, fmt, err); reportErr != nil {
		// Just log this error
		LOG("warning: couldn't report the daemon failure (%v)", reportErr)
	}
}

// TODO this does not work - Fix it with a recorder..
func createErrorEvent(ctx context.Context, rt *daemonRuntime, conf *daemonConfig, reasonFmt string, err error) error {
	DBG("logging event for error: %v", err)
	_, eventErr := rt.clientset.EventsV1beta1().Events(conf.Namespace).Create(ctx,
		&v1beta1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("file-integrity-operator-daemon.%v", v1.NowMicro()),
				Namespace: conf.Namespace,
			},
			Type:                corev1.EventTypeWarning,
			ReportingController: "file-integrity-operator-daemon",
			Reason:              fmt.Sprintf(reasonFmt, err),
		}, metav1.CreateOptions{})
	return eventErr
}

func reportDaemonError(ctx context.Context, rt *daemonRuntime, conf *daemonConfig, format string, err error) error {
	if reportErr := reportError(ctx, fmt.Sprintf(format, err), conf, rt); reportErr != nil {
		return reportErr
	}
	return createErrorEvent(ctx, rt, conf, format, err)
}

func runAideInitDBCmd(ctx context.Context, c *daemonConfig) error {
	configPath := aideConfigPath(c)

	return backoff.Retry(func() error {
		// CWE-78 - configPath is only made of user input during standalone debugging
		// #nosec
		cmd := exec.CommandContext(ctx, "aide", "-c", configPath, "-i")

		// Pre-load the MD5 guard for this *one* exec.
		// Append to, don’t overwrite, any existing LD_PRELOAD.
		env := os.Environ()
		env = append(env, "LD_PRELOAD="+common.MD5_GUARD_LIB)
		cmd.Env = env

		err := cmd.Run()
		exit := common.GetAideExitCode(err)

		switch exit {
		case common.AIDE_IO_ERROR:
			// Another AIDE instance still writing → back-off & retry.
			return err
		default:
			if err != nil {
				return backoff.Permanent(err)
			}
			return nil
		}
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))
}

func runAideScanCmd(ctx context.Context, c *daemonConfig) error {
	configPath := aideConfigPath(c)
	// CWE-78 - configPath is only made of user input during standalone debugging
	// #nosec
	return exec.CommandContext(ctx, "aide", "-c", configPath).Run()
}

func backUpAideFiles(c *daemonConfig) error {
	if err := backupReadDB(c); err != nil {
		return err
	}
	return backupReadLog(c)
}

func removeAideReinitFileIfExists(c *daemonConfig) error {
	// Clean up the legacy reinit file. Eventually this temporary file stuff will be replaced by a CRD.
	if err := removeFileIfExists(legacyAideReinitPath(c)); err != nil {
		return err
	}
	return removeFileIfExists(aideReinitPath(c))
}

func removeFileIfExists(filePath string) error {
	p := path.Clean(filePath)
	_, err := os.Stat(p)
	if err != nil && os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return os.Remove(p)
}

// updateAideLogFile copies the AIDE-written-to log file (aide.log.new) and copies it to the one our routines read
// (aide.log)
func updateAideLogFile(c *daemonConfig) error {
	return copyNonEmptyFile(aideWriteLogPath(c), aideReadLogPath(c))
}

// updateAideDBFiles copies the AIDE-written-to DB file (aide.db.gz.new) and copies it to the one our routines and
// AIDE check read from (aide.db.gz)
func updateAideDBFiles(c *daemonConfig) error {
	return copyNonEmptyFile(aideWriteDBPath(c), aideReadDBPath(c))
}

func updateAideLogFileIfPresent(c *daemonConfig) error {
	missingOrEmpty, err := fileIsMissingOrEmpty(aideWriteLogPath(c))
	if err != nil {
		return err
	}
	if missingOrEmpty {
		DBG("%s is missing or empty, did not copy", aideWriteLogPath(c))
		return nil
	}
	return updateAideLogFile(c)
}

func fileIsMissingOrEmpty(file string) (bool, error) {
	st, err := os.Stat(path.Clean(file))
	if err != nil && os.IsNotExist(err) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	if st.Size() <= 0 {
		return true, nil
	}
	return false, nil
}

// returns error if src is an empty, non-existent or non-regular file
func copyNonEmptyFile(src, dst string) error {
	DBG("copying %s to %s", src, dst)
	srcPath := path.Clean(src)
	sourceFileStat, err := os.Stat(srcPath)
	if err != nil {
		return err
	}

	if !sourceFileStat.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", srcPath)
	}

	if sourceFileStat.Size() <= 0 {
		return fmt.Errorf("%s is empty", srcPath)
	}

	source, err := os.Open(path.Clean(srcPath))
	if err != nil {
		return err
	}

	destination, err := os.OpenFile(path.Clean(dst), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		if err := source.Close(); err != nil {
			return err
		}
		return err
	}

	if _, err := io.Copy(destination, source); err != nil {
		if err := source.Close(); err != nil {
			return err
		}
		if err := destination.Close(); err != nil {
			return err
		}
		return err
	}

	if err := destination.Sync(); err != nil {
		if err := source.Close(); err != nil {
			return err
		}
		if err := destination.Close(); err != nil {
			return err
		}
		return err
	}

	if err := source.Close(); err != nil {
		return err
	}

	return destination.Close()
}

func backupReadLog(c *daemonConfig) error {
	readLog := aideReadLogPath(c)
	err := backupFile(readLog)
	if err != nil {
		return err
	}
	return pruneBackupFiles(c.FileDir, aideReadLogFileName, c.MaxBackups)
}

func backupReadDB(c *daemonConfig) error {
	readDB := aideReadDBPath(c)
	err := backupFile(readDB)
	if err != nil {
		return err
	}
	return pruneBackupFiles(c.FileDir, aideReadDBFileName, c.MaxBackups)
}

func backupFile(file string) error {
	missingOrEmpty, err := fileIsMissingOrEmpty(file)
	if err != nil {
		return err
	}
	if missingOrEmpty {
		DBG("%s is missing or empty, did not back-up", file)
		return nil
	}

	return copyNonEmptyFile(file, fmt.Sprintf("%s.backup-%s", file, time.Now().Format(backupTimeFormat)))
}

// Prune the oldest backup files (above max number) created by backupFile()
func pruneBackupFiles(dirPath, fileName string, max int) error {
	// find the related backup files
	files, err := ioutil.ReadDir(dirPath)
	if err != nil {
		return err
	}

	backups := []fs.FileInfo{}
	for i, _ := range files {
		f := files[i]
		if f.IsDir() {
			continue
		}
		if strings.HasPrefix(f.Name(), fmt.Sprintf("%s.backup-", fileName)) {
			backups = append(backups, files[i])
		}
	}

	if len(backups) <= max {
		// no pruning needed
		return nil
	}

	// Sort backups oldest first
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].ModTime().Unix() < backups[j].ModTime().Unix()
	})

	// Delete the older entries
	last := len(backups) - max
	for i, _ := range backups {
		if i < last {
			removeErr := os.Remove(path.Join(dirPath, backups[i].Name()))
			if removeErr != nil {
				LOG("error removing backup files: %s", removeErr)
				continue
			}
			DBG("pruned backup files - removed %s", path.Join(dirPath, backups[i].Name()))
		}
	}

	return nil
}

func getNonEmptyFile(filename string) *os.File {
	DBG("Opening %s", filename)

	// G304 (CWE-22) is addressed by this.
	cleanFileName := filepath.Clean(filename)

	// Note that we're cleaning the filename path above.
	// #nosec
	file, err := os.Open(cleanFileName)
	if err != nil {
		LOG("error opening log file: %v", err)
		return nil
	}

	fileinfo, err := file.Stat()
	// Only try to use the file if it already has contents.
	if err == nil && fileinfo.Size() > 0 {
		return file
	}

	return nil
}

// Might need this
//func initAideLog(c *daemonConfig) error {
//	f, err := os.Create(aideReadLogPath(c))
//	if err != nil {
//		return err
//	}
//	_, err = f.WriteString("\n")
//	if err != nil {
//		if err := f.Close(); err != nil {
//			return err
//		}
//		return err
//	}
//	if err := f.Sync(); err != nil {
//		if err := f.Close(); err != nil {
//			return err
//		}
//		return err
//	}
//	return f.Close()
//}
