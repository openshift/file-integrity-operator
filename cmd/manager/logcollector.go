/*
Copyright © 2019 Red Hat Inc.

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
	"io/ioutil"
	"sync"
	"time"
)

// logCollectorMainLoop creates temporary status report configMaps for the configmap controller to pick up and turn
// into permanent ones. It reads the last result reported from aide.
func logCollectorMainLoop(ctx context.Context, rt *daemonRuntime, conf *daemonConfig, errChan chan<- error, wg *sync.WaitGroup) {
	defer wg.Done()
	logCollectorCtx, logCollectorCancel := context.WithCancel(ctx)
	defer logCollectorCancel()

	for {
		select {
		// If one of the other loops returns an error (which causes the main routine to call cancel()), we hit this
		// case and exit. Does not block.
		case <-logCollectorCtx.Done():
			DBG("logCollectorLoop canceled by the main routine!")
			return
		// We've received a result. This will block when the result channel is empty, until our timeout case below.
		case lastResult := <-rt.GetResult():
			if lastResult == -1 || lastResult == 18 {
				// We haven't received a result yet.
				// Or return code 18 - AIDE ran prior to there being an aide database.
				DBG("No scan result available")
			} else if lastResult == 17 {
				// This is an AIDE config line error. We need to report this with an ERROR configMap without uploading
				// a log.
				logAndTryReportingDaemonError(logCollectorCtx, rt, conf, "AIDE error: %v", fmt.Errorf("17 Invalid configureline error"))
			} else if lastResult == 0 {
				// The check passed!
				if err := reportOK(logCollectorCtx, conf, rt); err != nil {
					// Considering this a non-fatal error right now.
					LOG("failed reporting scan result: %v", err)
				}
			} else {
				// Locking of the AIDE files is done in handleFailedResult()
				if !handleFailedResult(logCollectorCtx, rt, conf, errChan) {
					return
				}
			}
		// Since the result channel blocks when there is no result, this is our timeout case to continue
		case <-time.After(time.Duration(conf.Interval) * time.Second):
			// Just continue looping.
		}
	}
}

// handleFailedResult locks the AIDE files, reads, compresses (if needed) and uploads the failed AIDE log to a
// configMap for the operator to process. Fatal errors are sent to errChan, and returns false. Returns true on success.
func handleFailedResult(ctx context.Context, rt *daemonRuntime, conf *daemonConfig, errChan chan<- error) bool {
	rt.LockAideFiles("handleFailedResult")
	defer rt.UnlockAideFiles("handleFailedResult")
	DBG("AIDE check failed, continuing to collect log file")

	file := getNonEmptyFile(conf.LogCollectorFile)
	if file == nil {
		DBG("AIDE log file empty")
		return false
	}

	var compressedContents []byte

	fileInfo, err := file.Stat()
	if err != nil {
		logAndTryReportingDaemonError(ctx, rt, conf, "error getting AIDE log file information: %v", err)
		if closeErr := file.Close(); closeErr != nil {
			// Don't really care, just handle the error for gosec.
			LOG("warning: error closing file %v", closeErr)
		}

		errChan <- err
		return false
	}

	fileSize := fileInfo.Size()
	// Always read in the contents, when compressed we still need the uncompressed version to figure out
	// the changed details when updating the configMap later on.
	r := bufio.NewReader(file)
	contents, err := ioutil.ReadAll(r)
	if err != nil {
		logAndTryReportingDaemonError(ctx, rt, conf, "error reading AIDE log: %v", err)
		if closeErr := file.Close(); closeErr != nil {
			// Don't really care, just handle the error for gosec.
			LOG("warning: error closing file %v", closeErr)
		}

		errChan <- err
		return false
	}

	if closeErr := file.Close(); closeErr != nil {
		// Don't really care, just handle the error for gosec.
		LOG("warning: error closing file %v", closeErr)
	}

	if needsCompression(fileSize) || conf.LogCollectorCompress {
		DBG("compressing AIDE log contents")
		var compErr error
		compressedContents, compErr = compress(contents)
		if compErr != nil {
			logAndTryReportingDaemonError(ctx, rt, conf, "error compressing AIDE log: %v", err)
			errChan <- compErr
			return false
		}
	}

	if err := uploadLog(ctx, contents, compressedContents, conf, rt); err != nil {
		logAndTryReportingDaemonError(ctx, rt, conf, "error uploading AIDE log: %v", err)
		errChan <- err
	}
	return true
}
