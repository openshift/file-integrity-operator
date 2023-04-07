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

	for {
		select {
		case <-aideCtx.Done():
			DBG("aideLoop cancelled by the main routine!")
			return
		default:
			if !rt.Initializing() && !rt.Holding() {
				rt.LockAideFiles("aideLoop")
				LOG("running aide check")

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

				// All done. Send the result.
				rt.SendResult(aideResult)
				rt.UnlockAideFiles("aideLoop")
			}
			time.Sleep(time.Second * time.Duration(conf.Interval))
		}
	}
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

	if err := removeAideReinitFileIfExists(conf); err != nil {
		logAndTryReportingDaemonError(ctx, rt, conf, "Error removing the re-initialization file: %v", err)
		// What to clean up?  Probably same as above. Final initialization state is we have a freshly
		// copied reading db.
		// Might need to also clear out the reading db
		errChan <- err
		return err
	}
	LOG("initialization finished")
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
