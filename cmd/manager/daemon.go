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
	"net/http"
	"net/http/pprof"
	"os"
	"sync"
	"time"

	"github.com/openshift/file-integrity-operator/pkg/common"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/spf13/cobra"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	defaultAideFileDir   = "/hostroot/etc/kubernetes"
	defaultAideConfigDir = "/tmp"
	// Files for checks and reading. We copy the AIDE output files to here.
	aideReadDBFileName  = "aide.db.gz"
	aideReadLogFileName = "aide.log"
	// Files output from AIDE init and scan log. We copy the files AIDE writes from here.
	aideWritingDBFileName  = "aide.db.gz.new"
	aideWritingLogFileName = "aide.log.new"
	aideReinitFileName     = "aide.reinit"
	aideConfigFileName     = "aide.conf"
	backupTimeFormat       = "20060102T15_04_05"
	pprofAddr              = "127.0.0.1:6060"
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
	Local                     bool
	Pprof                     bool
	FileDir                   string
	ConfigDir                 string
}

type daemonRuntime struct {
	clientset         *kubernetes.Clientset
	dynclient         dynamic.Interface
	logCollectorInode uint64
	initializing      bool
	initializingMux   sync.Mutex
	holding           bool
	holdingMux        sync.Mutex
	result            chan int
	dbMux             sync.Mutex
	fiInstance        *unstructured.Unstructured
	instanceMux       sync.Mutex
}

func (rt *daemonRuntime) Initializing() bool {
	rt.initializingMux.Lock()
	defer rt.initializingMux.Unlock()
	return rt.initializing
}

// Initializing is how the initLoop tells the aideLoop to wait for a new database update.
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
	rt.dbMux.Lock()
	// Log after, since Lock() blocks. This way the lock messages are more timely.
	DBG("aide files locked by %s", fun)
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

func (rt *daemonRuntime) SendResult(result int) {
	rt.result <- result
}

func (rt *daemonRuntime) GetResult() <-chan int {
	return rt.result
}

var debugLog bool

func LOG(format string, a ...interface{}) {
	fmt.Printf(fmt.Sprintf("%s: %s\n", time.Now().Format(time.RFC3339), format), a...)
}

func DBG(format string, a ...interface{}) {
	if debugLog {
		LOG("debug: "+format, a...)
	}
}

func FATAL(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL:"+format+"\n", a...)
	os.Exit(1)
}

func defineFlags(cmd *cobra.Command) {
	cmd.Flags().String("lc-file", "", "The log file to collect.")
	cmd.Flags().String("owner", "", "The FileIntegrity object to set as owner of the created configMap objects.")
	cmd.Flags().String("lc-config-map-prefix", "", "Prefix for the configMap name, typically the podname.")
	cmd.Flags().String("namespace", "", "Namespace")
	cmd.Flags().String("aidefiledir", defaultAideFileDir, "The directory where the daemon will look for AIDE runtime files. Should only be changed when debugging.")
	cmd.Flags().String("aideconfigdir", defaultAideConfigDir, "The directory where the daemon will look for the AIDE config. Should only be changed when debugging.")
	cmd.Flags().Int64("lc-timeout", defaultTimeout, "How long to poll for the log and indicator files in seconds.")
	cmd.Flags().Int64("interval", common.DefaultGracePeriod, "How often to recheck for AIDE results.")
	cmd.Flags().Bool("lc-compress", false, "Use gzip+base64 to compress the log file contents.")
	cmd.Flags().Bool("debug", false, "Print debug messages")
	cmd.Flags().Bool("local", false, "Run the daemon locally, using KUBECONFIG. Should only be used when debugging.")
	cmd.Flags().Bool("pprof", false, "Enable /debug/pprof endpoints. Should only be used when debugging.")
}

func parseDaemonConfig(cmd *cobra.Command) *daemonConfig {
	var conf daemonConfig
	conf.LogCollectorFile = getValidStringArg(cmd, "lc-file")
	conf.FileIntegrityName = getValidStringArg(cmd, "owner")
	conf.Namespace = getValidStringArg(cmd, "namespace")
	conf.FileDir = getValidStringArg(cmd, "aidefiledir")
	conf.ConfigDir = getValidStringArg(cmd, "aideconfigdir")
	conf.LogCollectorNode = os.Getenv("NODE_NAME")
	conf.LogCollectorConfigMapName = getConfigMapName(getValidStringArg(cmd, "lc-config-map-prefix"), conf.LogCollectorNode)
	conf.LogCollectorTimeout, _ = cmd.Flags().GetInt64("lc-timeout")
	conf.Interval, _ = cmd.Flags().GetInt64("interval")
	conf.LogCollectorCompress, _ = cmd.Flags().GetBool("lc-compress")
	debugLog, _ = cmd.Flags().GetBool("debug")
	conf.Local, _ = cmd.Flags().GetBool("local")
	conf.Pprof, _ = cmd.Flags().GetBool("pprof")
	return &conf
}

func newDaemonRuntime(conf *daemonConfig) *daemonRuntime {
	kc := ""
	if conf.Local {
		kc = os.Getenv("KUBECONFIG")
		DBG("Using KUBECONFIG=%s", kc)
	}
	// Falls back to InClusterConfig if not running locally
	config, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		FATAL("%v", err)
	}
	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		FATAL("%v", err)
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		FATAL("%v", err)
	}

	return &daemonRuntime{
		clientset:         clientSet,
		dynclient:         dynamicClient,
		logCollectorInode: 0,
	}
}

func startMemoryProfiling(conf *daemonConfig) {
	if conf.Pprof {
		DBG("Starting pprof endpoint at %s/debug/pprof/", pprofAddr)
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		go http.ListenAndServe(pprofAddr, mux)
	}
}

func setInitialRuntimeState(rt *daemonRuntime) {
	// A note about the initial state: since a buffered channel's recv (logCollectorMainLoop()'s last-result input)
	// blocks when the buffer is empty, we want it empty to start with. We used to load rt.result with -1 to avoid a
	// race, but now we need to do the opposite.
	rt.result = make(chan int, 50)
	rt.SetInitializing("main", false)
}

func daemonMainLoop(cmd *cobra.Command, args []string) {
	LOG("Starting the AIDE runner daemon")
	var wg sync.WaitGroup

	conf := parseDaemonConfig(cmd)
	rt := newDaemonRuntime(conf)
	startMemoryProfiling(conf)
	setInitialRuntimeState(rt)

	errChan := make(chan error)
	ctx, cancel := context.WithCancel(context.Background())

	// integrityInstanceLoop is not added to the waitgroup because the watcher would stall wg.Wait() indefinitely. It
	// will just die with the process and does not have anything to clean up.
	go integrityInstanceLoop(ctx, rt, conf, errChan)
	wg.Add(4)
	go reinitLoop(ctx, rt, conf, errChan, &wg)
	go holdOffLoop(ctx, rt, conf, errChan, &wg)
	go aideLoop(ctx, rt, conf, errChan, &wg)
	go logCollectorMainLoop(ctx, rt, conf, errChan, &wg)

	var finalErr error
	for {
		select {
		case <-ctx.Done():
			DBG("exiting.. waiting for goroutines to finish")
			wg.Wait()
			FATAL("exit: %v", finalErr)
		case err := <-errChan:
			finalErr = err
			DBG("cancelling main routine")
			cancel()
		}
	}
}
