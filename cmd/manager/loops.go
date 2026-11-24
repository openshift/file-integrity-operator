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
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	watch2 "k8s.io/client-go/tools/watch"

	"github.com/openshift/file-integrity-operator/pkg/common"
)

// The aide loop runs the aide check at interval, unless the node controller holds it, or the reinitLoop is
// initializing or re-initializing. The result is saved to be read by logCollectorMainLoop.
func aideLoop(ctx context.Context, rt *daemonRuntime, conf *daemonConfig, errChan chan<- error, wg *sync.WaitGroup) {
	defer wg.Done()
	aideCtx, aideCancel := context.WithCancel(ctx)
	defer aideCancel()

	interval := time.Duration(conf.Interval) * time.Second
	// When the last check finished. The loop re-evaluates every second instead of sleeping a whole interval, so the
	// checks resume as soon as a holdoff or an initialization allows it.
	var lastScan time.Time
	for {
		select {
		case <-aideCtx.Done():
			DBG("aideLoop cancelled by the main routine!")
			return
		default:
			if scanAllowed(rt, conf) && scanDue(lastScan, rt.LastInit(), time.Now(), interval) {
				rt.LockAideFiles("aideLoop")
				LOG("running aide check")
				scanStart := time.Now()

				// Run the actual AIDE scan command.
				// This doesn't handle the output, because the operator ensures AIDE logs to /hostroot/etc/kubernetes/aide.log.new
				aideResult := common.GetAideExitCode(runAideScanCmd(aideCtx, conf))
				LOG("aide check returned status %d", aideResult)

				// The scan has finished, now we need to update the files to be handled by the logCollector.
				if fileErr := updateAideLogFileIfPresent(conf); fileErr != nil {
					logAndTryReportingDaemonError(aideCtx, rt, conf, "Error updating log files from scan: %v", fileErr)

					// We failed to do intermediate daemon steps.. Send the error to the main routine and quit.
					errChan <- fileErr
					rt.UnlockAideFiles("aideLoop")
					return
				}

				lastScan = time.Now()

				// The check was not held when it started, so holding now, or a holding that ended since, means a node
				// update started during the check, and its changes would be reported as a failure. The database is
				// re-initialized after the update instead.
				if rt.Holding() || rt.NotHoldingSince().After(scanStart) {
					LOG("discarding the aide check result, a node update started during the check")
				} else {
					// All done. Send the result.
					rt.SendResult(aideResult)
				}
				rt.UnlockAideFiles("aideLoop")

				// AIDE reads the entire host filesystem, and the resulting page
				// cache is charged to this container's cgroup. Reclaim it so
				// reported memory drops back to the daemon's actual working set.
				reclaimCgroupPageCache()
				releaseMemoryAfterScan()
			}
			time.Sleep(time.Second)
		}
	}
}

// scanAllowed returns whether the aideLoop can run the aide check now. It cannot while the database is being
// initialized, while the node is held off, or once the holdoff is over until the database is re-initialized, as the
// files changed by the node update would fail the check until then. If that re-init does not come within
// holdoffReinitTimeout, the checks resume without it.
func scanAllowed(rt *daemonRuntime, conf *daemonConfig) bool {
	if rt.Initializing() || rt.Holding() {
		return false
	}
	if !holdoffFileExists(conf) {
		return true
	}
	if time.Since(rt.NotHoldingSince()) < holdoffReinitTimeout {
		return false
	}
	LOG("no AIDE database re-initialization within %v after the holdoff, resuming the aide checks", holdoffReinitTimeout)
	if err := removeFileIfExists(aideHoldoffPath(conf)); err != nil {
		LOG("warning: error removing the holdoff file: %v", err)
	}
	return true
}

// scanDue returns whether the next aide check is due. The first one runs right away, and the next ones once every
// interval. A database (re-)initialization makes it due right away too, so that the node status reflects the new
// database instead of the last check against the old one, e.g. a failure during a node update.
func scanDue(lastScan, lastInit, now time.Time, interval time.Duration) bool {
	return lastScan.IsZero() || lastInit.After(lastScan) || now.Sub(lastScan) >= interval
}

// The holdoff file is the signal from the node controller to pause the aide scan.
// We do not make this pause the logCollector loop, and we might want to.
func holdOffLoop(ctx context.Context, rt *daemonRuntime, conf *daemonConfig, errChan chan<- error, wg *sync.WaitGroup) {
	defer wg.Done()
	holdOffCtx, holdOffcancel := context.WithCancel(ctx)
	defer holdOffcancel()

	for {
		select {
		case <-holdOffCtx.Done():
			DBG("holdOffLoop cancelled by the main routine!")
			return
		default:
			fi := rt.GetFileIntegrityInstance()
			annotations := fi.GetAnnotations()
			if annotations == nil {
				// No need to hold off since there is no annotations
				rt.SetHolding("holdOffLoop", false)
			} else {
				// we need to get name of the node we are running on
				nodeName := os.Getenv("NODE_NAME")
				if nodeName == "" {
					err := fmt.Errorf("NODE_NAME environment variable not set")
					logAndTryReportingDaemonError(holdOffCtx, rt, conf, "Error getting node name: %v", err)
					errChan <- err
					return
				}
				if nodeList, foundHoldOff := annotations[common.IntegrityHoldoffAnnotationKey]; foundHoldOff {
					nodeInList := false
					for _, node := range strings.Split(nodeList, ",") {
						if node == nodeName {
							nodeInList = true
							break
						}
					}
					if nodeList == "" || nodeInList {
						// All nodes are in holdoff
						if err := createHoldoffFileIfMissing(conf); err != nil {
							LOG("warning: error creating the holdoff file: %v", err)
						}
						rt.SetHolding("holdOffLoop", true)
					} else {
						rt.SetHolding("holdOffLoop", false)
					}
				} else {
					// No need to hold off since there is no annotation
					rt.SetHolding("holdOffLoop", false)
				}
			}
			time.Sleep(time.Second)
		}
	}
}

// The reinitLoop initializes the aide DB if they do not exist, or if the re-init signal file has been placed on the
// node by the reinit daemonSet spawned by the fileIntegrity controller.
func reinitLoop(ctx context.Context, rt *daemonRuntime, conf *daemonConfig, errChan chan<- error, wg *sync.WaitGroup) {
	defer wg.Done()
	reinitCtx, reinitCancel := context.WithCancel(ctx)
	defer reinitCancel()

	for {
		select {
		case <-reinitCtx.Done():
			DBG("reinitLoop cancelled by the main routine!")
			return
		default:
			// Whether the node is held off decides if an initialization ends a holdoff, so wait until it is known.
			if !rt.HoldingKnown() {
				time.Sleep(time.Second)
				continue
			}
			readDBStat, readDBStatErr := os.Stat(aideReadDBPath(conf))
			_, initStatErr := os.Stat(aideReinitPath(conf))

			//_, writeDBStatErr := os.Stat(aideWriteDBPath(conf))
			// We don't care about the writing db when checking if we need initialization. The reading DB needs to be
			// in place for the AIDE check, so if we have a missing or empty one we need to initialize.
			// Otherwise, if we have the aide re-init trigger file in place, that means we need to force initialize.
			if os.IsNotExist(readDBStatErr) || readDBStat.Size() <= 0 || initStatErr == nil {
				if err := handleAIDEInit(reinitCtx, rt, conf, errChan, initStatErr == nil); err != nil {
					return
				}
			}
			time.Sleep(time.Second)
		}
	}
}

// handleAIDEInit locks and reinitializes the AIDE files, reporting fatal errors to errChan.
func handleAIDEInit(ctx context.Context, rt *daemonRuntime, conf *daemonConfig, errChan chan<- error, isReinit bool) error {
	rt.SetInitializing("handleAIDEInit", true)
	defer rt.SetInitializing("handleAIDEInit", false)

	// remove the reinit trigger file as soon as we start initialization
	// so that we can take care of any future reinit triggers
	if err := removeAideReinitFileIfExists(conf); err != nil {
		logAndTryReportingDaemonError(ctx, rt, conf, "Error removing the re-initialization file: %v", err)
		// What to clean up?  Probably same as above. Final initialization state is we have a freshly
		// copied reading db.
		// Might need to also clear out the reading db
		errChan <- err
		return err
	}

	rt.LockAideFiles("handleAIDEInit")
	defer rt.UnlockAideFiles("handleAIDEInit")

	if isReinit {
		LOG("force-initializing AIDE db")
	} else {
		LOG("initializing AIDE db")
	}

	// Back up files, if we need to.
	if err := backUpAideFiles(conf); err != nil {
		logAndTryReportingDaemonError(ctx, rt, conf, "error backing up files during initialization: %v", err)
		// There's no initialization state to rewind at this point.
		errChan <- err
		return err
	}

	// An initialization outside of a holdoff ends the wait for the re-init that follows one. Remove the holdoff file
	// before AIDE builds the database, so that it is never recorded in it, whatever the AIDE config excludes. A
	// database initialized while the node is held off may capture the middle of a node update, so it keeps the file.
	if !rt.Holding() {
		if err := removeFileIfExists(aideHoldoffPath(conf)); err != nil {
			LOG("warning: error removing the holdoff file: %v", err)
		}
	}

	if err := runAideInitDBCmd(ctx, conf); err != nil {
		aideRv := common.GetAideExitCode(err)
		logAndTryReportingDaemonError(ctx, rt, conf, fmt.Sprintf("Error initializing the AIDE DB: %s",
			common.GetAideErrorMessage(aideRv))+" %v", err)
		// What to clean up? The most we would likely get here during a failure is an incomplete writing db
		// Kill/Zero out the writing db. This will depend on AIDE behavior
		errChan <- err
		return err
	}

	if err := updateAideDBFiles(conf); err != nil {
		// TODO: This could be an error 17, invalid AIDE configuration, meaning the admin has to fix the provided config.
		// This is fatal for the daemon and will make it error, but after fixing the config, the operator will force
		// it to restart. Okay for now, but instead we might want to continue gracefully.
		logAndTryReportingDaemonError(ctx, rt, conf, "Error updating the AIDE db files: %v", err)
		// What to clean up?  Probably same as above. Final initialization state is we have a freshly
		// copied reading db.
		// Might need to also clear out the reading db
		errChan <- err
		return err
	}

	LOG("initialization finished")
	rt.SetLastInit(time.Now())
	reclaimCgroupPageCache()
	releaseMemoryAfterScan()
	return nil
}

func integrityInstanceLoop(ctx context.Context, rt *daemonRuntime, conf *daemonConfig, errChan chan<- error) {
	integrityCtx, integrityCancel := context.WithCancel(ctx)
	defer integrityCancel()

	DBG("Getting FileIntegrity %s/%s", conf.Namespace, conf.FileIntegrityName)

	fiResource := schema.GroupVersionResource{
		Group:    crdGroup,
		Version:  crdAPIVersion,
		Resource: crdPlurals,
	}

	var initialVersion string
	err := backoff.Retry(func() error {
		// Set initial instance
		fi, err := rt.dynclient.Resource(fiResource).Namespace(conf.Namespace).Get(integrityCtx, conf.FileIntegrityName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		rt.SetFileIntegrityInstance(fi)
		initialVersion = fi.GetResourceVersion()
		return nil
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))

	if err != nil {
		errChan <- err
		return
	}

	listOpts := metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.name", conf.FileIntegrityName).String(),
	}
	listWatcher := &cache.ListWatch{
		WatchFunc: func(options v1.ListOptions) (watch.Interface, error) {
			return rt.dynclient.Resource(fiResource).Namespace(conf.Namespace).Watch(integrityCtx, listOpts)
		},
		ListFunc: func(options v1.ListOptions) (runtime.Object, error) {
			return rt.dynclient.Resource(fiResource).Namespace(conf.Namespace).List(integrityCtx, listOpts)
		},
	}

	watcher, err := watch2.NewRetryWatcher(initialVersion, listWatcher)
	if err != nil {
		errChan <- err
		return
	}

	ch := watcher.ResultChan()
	for event := range ch {
		if event.Type == watch.Error {
			DBG("Got an error from watching the file integrity object: %v", event.Object)
			continue
		}
		fi, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			DBG("Could not cast the integrity object as unstructured: %v", event.Object)
			continue
		}
		rt.SetFileIntegrityInstance(fi.DeepCopy())
	}

	errChan <- fmt.Errorf("reached the end of FileIntegrity instance loop. should not happen")
	return
}
