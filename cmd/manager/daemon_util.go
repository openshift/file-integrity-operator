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
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"

	backoff "github.com/cenkalti/backoff/v4"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/api/events/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/file-integrity-operator/pkg/common"
)

// reclaimCgroupPageCache asks the kernel to reclaim file-backed (page cache)
// memory charged to this container's cgroup. AIDE scans the entire host
// filesystem, and the resulting page cache pages are charged to the container's
// cgroup, causing reported memory to grow toward the limit over scan cycles.
//
// We use raw syscalls instead of os.OpenFile because Go's runtime registers
// opened fds with its epoll-based poller. The cgroup v2 memory.reclaim file
// supports poll (via cgroup_file_poll), so Go treats it as a pollable fd and
// waits for write-readiness before issuing the write. That readiness event
// never arrives, hanging the goroutine permanently.
func reclaimCgroupPageCache() {
	cgroupPath, err := getOwnCgroupPath()
	if err != nil {
		LOG("could not determine own cgroup path (page cache not reclaimed): %v", err)
		return
	}

	reclaimFile := path.Join(cgroupPath, "memory.reclaim")
	fd, err := syscall.Open(reclaimFile, syscall.O_WRONLY, 0)
	if err != nil {
		LOG("memory.reclaim not available at %s (page cache not reclaimed): %v", reclaimFile, err)
		return
	}

	_, err = syscall.Write(fd, []byte("500M"))
	closeErr := syscall.Close(fd)
	if err != nil && err != syscall.EAGAIN {
		LOG("memory.reclaim write returned (non-fatal): %v", err)
	}
	if closeErr != nil {
		LOG("memory.reclaim close error: %v", closeErr)
	}
	LOG("reclaimed cgroup page cache after AIDE scan")
}

// getOwnCgroupPath reads /proc/self/cgroup (cgroup v2 unified format) and
// returns the sysfs path for this process's cgroup.
func getOwnCgroupPath() (string, error) {
	f, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// cgroup v2: "0::<path>"
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[0] == "0" {
			return "/sys/fs/cgroup" + parts[2], nil
		}
	}
	return "", fmt.Errorf("no cgroup v2 entry found in /proc/self/cgroup")
}

// releaseMemoryAfterScan forces the Go GC to run and returns freed memory to
// the OS. Combined with reclaimCgroupPageCache, this minimizes the container's
// memory footprint between scan cycles.
func releaseMemoryAfterScan() {
	runtime.GC()
	debug.FreeOSMemory()
}

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
	files, err := os.ReadDir(dirPath)
	if err != nil {
		return err
	}

	type backupEntry struct {
		name    string
		modTime int64
	}

	var backups []backupEntry
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if strings.HasPrefix(f.Name(), fmt.Sprintf("%s.backup-", fileName)) {
			info, err := f.Info()
			if err != nil {
				LOG("error reading backup file info: %s", err)
				continue
			}
			backups = append(backups, backupEntry{name: f.Name(), modTime: info.ModTime().Unix()})
		}
	}

	if len(backups) <= max {
		// no pruning needed
		return nil
	}

	// Sort backups oldest first
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].modTime < backups[j].modTime
	})

	// Delete the older entries
	last := len(backups) - max
	for i := range backups {
		if i < last {
			removeErr := os.Remove(path.Join(dirPath, backups[i].name))
			if removeErr != nil {
				LOG("error removing backup files: %s", removeErr)
				continue
			}
			DBG("pruned backup files - removed %s", path.Join(dirPath, backups[i].name))
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

	if err := file.Close(); err != nil {
		LOG("warning: error closing empty/unreadable file %s: %v", cleanFileName, err)
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
