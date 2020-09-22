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
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"k8s.io/api/events/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/cenkalti/backoff/v3"
	"github.com/openshift/file-integrity-operator/pkg/common"

	"github.com/spf13/cobra"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	aideDBPath       = "/hostroot/etc/kubernetes/aide.db.gz"
	aideLogPath      = "/hostroot/etc/kubernetes/aide.log"
	backupTimeFormat = "20060102T15_04_05"
	aideReinitFile   = "/hostroot/etc/kubernetes/aide.reinit"
	aideHoldoffFile  = "/hostroot/etc/kubernetes/holdoff"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "daemon",
	Long:  `The file-integrity-operator daemon subcommand.`,
	Run:   daemonMainLoop,
}

func init() {
	defineFlags(daemonCmd)
}

type daemonConfig struct {
	LogCollectorFile          string
	FileIntegrityName         string
	LogCollectorConfigMapName string
	Namespace                 string
	LogCollectorNode          string
	LogCollectorTimeout       int64
	Interval                  int64
	LogCollectorCompress      bool
}

type daemonRuntime struct {
	clientset              *kubernetes.Clientset
	dynclient              dynamic.Interface
	logCollectorInode      uint64
	logCollectorReadoffset int64
	initializing           bool
	initializingMux        sync.Mutex
	holding                bool
	holdingMux             sync.Mutex
	result                 chan int
	dbMux                  sync.Mutex
	fiInstance             *unstructured.Unstructured
	instanceMux            sync.Mutex
}

func (rt *daemonRuntime) Initializing() bool {
	rt.initializingMux.Lock()
	defer rt.initializingMux.Unlock()
	return rt.initializing
}

func (rt *daemonRuntime) SetInitializing(fun string, initializing bool) {
	rt.initializingMux.Lock()
	if initializing != rt.initializing {
		DBG("initializing set to %v by %s", initializing, fun)
	}
	rt.initializing = initializing
	rt.initializingMux.Unlock()
}

func (rt *daemonRuntime) Holding() bool {
	rt.holdingMux.Lock()
	defer rt.holdingMux.Unlock()
	return rt.holding
}

func (rt *daemonRuntime) SetHolding(fun string, holding bool) {
	rt.holdingMux.Lock()
	if holding != rt.holding {
		DBG("holding set to %v by %s", holding, fun)
	}
	rt.holding = holding
	rt.holdingMux.Unlock()
}

func (rt *daemonRuntime) LockAideFiles(fun string) {
	DBG("aide files locked by %s", fun)
	rt.dbMux.Lock()
}

func (rt *daemonRuntime) UnlockAideFiles(fun string) {
	DBG("aide files unlocked by %s", fun)
	rt.dbMux.Unlock()
}

func (rt *daemonRuntime) GetFileIntegrityInstance() *unstructured.Unstructured {
	for {
		rt.instanceMux.Lock()
		if rt.fiInstance == nil {
			rt.instanceMux.Unlock()
			DBG("Still waiting for file integrity instance initialization")
			time.Sleep(time.Second)
			continue
		}
		defer rt.instanceMux.Unlock()
		return rt.fiInstance
	}
}

func (rt *daemonRuntime) SetFileIntegrityInstance(fi *unstructured.Unstructured) {
	rt.instanceMux.Lock()
	rt.fiInstance = fi
	rt.instanceMux.Unlock()
}

func defineFlags(cmd *cobra.Command) {
	cmd.Flags().String("lc-file", "", "The log file to collect.")
	cmd.Flags().String("owner", "", "The FileIntegrity object to set as owner of the created configMap objects.")
	cmd.Flags().String("lc-config-map-prefix", "", "Prefix for the configMap name, typically the podname.")
	cmd.Flags().String("namespace", "Running pod namespace.", ".")
	cmd.Flags().Int64("lc-timeout", defaultTimeout, "How long to poll for the log and indicator files in seconds.")
	cmd.Flags().Int64("interval", common.DefaultGracePeriod, "How often to recheck for AIDE results.")
	cmd.Flags().Bool("lc-compress", false, "Use gzip+base64 to compress the log file contents.")
	cmd.Flags().Bool("debug", false, "Print debug messages")
}

func parseDaemonConfig(cmd *cobra.Command) *daemonConfig {
	var conf daemonConfig
	conf.LogCollectorFile = getValidStringArg(cmd, "lc-file")
	conf.FileIntegrityName = getValidStringArg(cmd, "owner")
	conf.Namespace = getValidStringArg(cmd, "namespace")
	conf.LogCollectorNode = os.Getenv("NODE_NAME")
	conf.LogCollectorConfigMapName = getConfigMapName(getValidStringArg(cmd, "lc-config-map-prefix"), conf.LogCollectorNode)
	conf.LogCollectorTimeout, _ = cmd.Flags().GetInt64("lc-timeout")
	conf.Interval, _ = cmd.Flags().GetInt64("interval")
	conf.LogCollectorCompress, _ = cmd.Flags().GetBool("lc-compress")
	debugLog, _ = cmd.Flags().GetBool("debug")
	return &conf
}

func daemonMainLoop(cmd *cobra.Command, args []string) {
	conf := parseDaemonConfig(cmd)
	LOG("Starting the AIDE runner daemon")

	config, err := rest.InClusterConfig()
	if err != nil {
		FATAL("%v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		FATAL("%v", err)
	}
	dynclient, err := dynamic.NewForConfig(config)
	if err != nil {
		FATAL("%v", err)
	}

	rt := &daemonRuntime{
		clientset: clientset,
		dynclient: dynclient,
	}

	rt.result = make(chan int, 50)

	// Set initial states so the loops do not race in the beginning.
	rt.result <- -1
	rt.SetInitializing("main", false)

	reinitLoopDone := make(chan bool)
	holdOffLoopDone := make(chan bool)
	aideLoopDone := make(chan bool)
	logCollectorLoopDone := make(chan bool)
	integrityInstanceLoopDone := make(chan bool)
	go integrityInstanceLoop(rt, conf, integrityInstanceLoopDone)
	go reinitLoop(rt, conf, reinitLoopDone)
	go holdOffLoop(rt, conf, holdOffLoopDone)
	go aideLoop(rt, conf, aideLoopDone)
	go logCollectorMainLoop(rt, conf, logCollectorLoopDone)

	// At the moment only reinitLoop exits fatally,
	select {
	case <-reinitLoopDone:
		FATAL("%v", fmt.Errorf("re-init errored"))
	case <-holdOffLoopDone:
		FATAL("%v", fmt.Errorf("holdoff loop errored"))
	case <-aideLoopDone:
		FATAL("%v", fmt.Errorf("aide errored"))
	case <-logCollectorLoopDone:
		FATAL("%v", fmt.Errorf("log-collector errored"))
	case <-integrityInstanceLoopDone:
		FATAL("%v", fmt.Errorf("instance watcher errored"))
	}
}

// The aide loop runs the aide check at interval, unless the node controller holds it, or the reinitLoop is
// initializing or re-initializing. The result is saved to be read by logCollectorMainLoop.
func aideLoop(rt *daemonRuntime, conf *daemonConfig, exit chan bool) {
	for {
		if !rt.Initializing() && !rt.Holding() {
			rt.LockAideFiles("aideLoop")
			LOG("running aide check")
			// This doesn't handle the output, because the operator ensures AIDE logs to /hostroot/etc/kubernetes/aide.log
			err := runAideScanCmd()
			exitStatus := common.GetAideExitCode(err)
			LOG("aide check returned status %d", exitStatus)
			rt.result <- exitStatus
			rt.UnlockAideFiles("aideLoop")
		}
		time.Sleep(time.Second * time.Duration(conf.Interval))
	}
}

// The holdoff file is the signal from the node controller to pause the aide scan.
// We do not make this pause the logCollector loop, and we might want to.
func holdOffLoop(rt *daemonRuntime, conf *daemonConfig, exit chan bool) {
	for {
		fi := rt.GetFileIntegrityInstance()
		annotations := fi.GetAnnotations()
		if annotations == nil {
			// No need to hold off since there is no annotations
			rt.SetHolding("holdOffLoop", false)
		} else {
			_, foundHoldOff := annotations[common.IntegrityHoldoffAnnotationKey]
			if foundHoldOff {
				rt.SetHolding("holdOffLoop", true)
			} else {
				rt.SetHolding("holdOffLoop", false)
			}
		}
		time.Sleep(time.Second)
	}
}

// The reinitLoop initializes the aide DB if they do not exist, or if the re-init signal file has been placed on the
// node by the reinit daemonSet spawned by the fileIntegrity controller.
func reinitLoop(rt *daemonRuntime, conf *daemonConfig, exit chan bool) {
	for {
		_, dbStatErr := os.Stat(aideDBPath)
		_, initStatErr := os.Stat(aideReinitFile)
		if os.IsNotExist(dbStatErr) {
			rt.SetInitializing("reinitLoop", true)
			rt.LockAideFiles("reinitLoop")
			LOG("initializing aide")
			if err := runAideInitDBCmd(); err != nil {
				LOG(err.Error())
				time.Sleep(time.Second)
				rt.UnlockAideFiles("reinitLoop")
				continue
			}
			LOG("initialization finished")
			rt.UnlockAideFiles("reinitLoop")
		} else if initStatErr == nil {
			rt.SetInitializing("reinitLoop", true)
			rt.LockAideFiles("reinitLoop")
			LOG("re-initializing aide")

			if err := backUpAideFiles(); err != nil {
				LOG(err.Error())
				_, eventErr := rt.clientset.EventsV1beta1().Events(conf.Namespace).Create(context.TODO(), &v1beta1.Event{
					EventTime:           v1.NowMicro(),
					ReportingController: "file-integrity-operator-daemon",
					Reason:              fmt.Sprintf("Error backing up the aide files: %v", err),
				}, v1.CreateOptions{})
				if eventErr != nil {
					LOG("error creating error event %v", eventErr)
				}
				// Fatal error
				exit <- true
				continue
			}

			if err := initAideLog(); err != nil {
				LOG(err.Error())
				_, eventErr := rt.clientset.EventsV1beta1().Events(conf.Namespace).Create(context.TODO(), &v1beta1.Event{
					EventTime:           v1.NowMicro(),
					ReportingController: "file-integrity-operator-daemon",
					Reason:              fmt.Sprintf("Error initializing the aide log: %v", err),
				}, v1.CreateOptions{})
				if eventErr != nil {
					LOG("error creating error event %v", eventErr)
				}
				// Fatal error
				exit <- true
				continue
			}

			if err := runAideInitDBCmd(); err != nil {
				LOG(err.Error())
				time.Sleep(time.Second)
				rt.UnlockAideFiles("reinitLoop")
				continue
			}

			if err := removeAideReinitFile(); err != nil {
				LOG(err.Error())
				_, eventErr := rt.clientset.EventsV1beta1().Events(conf.Namespace).Create(context.TODO(), &v1beta1.Event{
					EventTime:           v1.NowMicro(),
					ReportingController: "file-integrity-operator-daemon",
					Reason:              fmt.Sprintf("Error removing the re-initialization file: %v", err),
				}, v1.CreateOptions{})
				if eventErr != nil {
					LOG("error creating error event %v", eventErr)
				}
				time.Sleep(time.Second)
				continue
			}

			LOG("re-initialization finished")
			rt.UnlockAideFiles("reinitLoop")
		}
		rt.SetInitializing("reinitLoop", false)
		time.Sleep(time.Second)
	}
}

func integrityInstanceLoop(rt *daemonRuntime, conf *daemonConfig, exit chan bool) {
	DBG("Getting FileIntegrity %s/%s", conf.Namespace, conf.FileIntegrityName)

	fiResource := schema.GroupVersionResource{
		Group:    crdGroup,
		Version:  crdAPIVersion,
		Resource: crdPlurals,
	}

	err := backoff.Retry(func() error {
		// Set initial instance
		fi, err := rt.dynclient.Resource(fiResource).Namespace(conf.Namespace).Get(context.TODO(), conf.FileIntegrityName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		rt.SetFileIntegrityInstance(fi)
		return nil
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))

	if err != nil {
		DBG("Couldn't get file integrity object: %s", conf.FileIntegrityName)
		exit <- true
	}

	listopts := metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.name", conf.FileIntegrityName).String(),
	}
	watcher, err := rt.dynclient.Resource(fiResource).Namespace(conf.Namespace).Watch(context.TODO(), listopts)
	if err != nil {
		DBG("Couldn't watch file integrity object: %s", conf.FileIntegrityName)
		exit <- true
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

	exit <- true
}

func runAideInitDBCmd() error {
	return exec.Command("aide", "-c", "/tmp/aide.conf", "-i").Run()
}

func runAideScanCmd() error {
	return exec.Command("aide", "-c", "/tmp/aide.conf").Run()
}

func backUpAideFiles() error {
	if err := backupFile(aideDBPath); err != nil {
		return err
	}
	return backupFile(aideLogPath)
}

func removeAideReinitFile() error {
	return os.Remove(aideReinitFile)
}

func backupFile(file string) error {
	return os.Rename(file, fmt.Sprintf("%s.backup-%s", file, time.Now().Format(backupTimeFormat)))
}

func initAideLog() error {
	f, err := os.Create(aideLogPath)
	if err != nil {
		return err
	}
	_, err = f.WriteString("\n")
	if err != nil {
		if err := f.Close(); err != nil {
			return err
		}
		return err
	}
	if err := f.Sync(); err != nil {
		if err := f.Close(); err != nil {
			return err
		}
		return err
	}
	return f.Close()
}
