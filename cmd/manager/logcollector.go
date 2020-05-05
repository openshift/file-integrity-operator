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
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	backoff "github.com/cenkalti/backoff/v3"
	"github.com/spf13/cobra"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openshift/file-integrity-operator/pkg/common"
)

const (
	crdGroup      = "file-integrity.openshift.io"
	crdAPIVersion = "v1alpha1"
	crdPlurals    = "fileintegrities"
	maxRetries    = 15
	// These need to be lessened for normal use.
	defaultTimeout       = 600
	defaultInterval      = 1800
	defaultIndicatorFile = "/hostroot/etc/kubernetes/aide.latest-result.log"
	uncompressedMaxSize  = 1048570 // 1MB for etcd limit
)

var logCollectorCmd = &cobra.Command{
	Use:   "logcollector",
	Short: "logcollector",
	Long:  `The file-integrity-operator logcollector subcommand.`,
	Run:   logCollectorMainLoop,
}

func init() {
	defineFlags(logCollectorCmd)
}

type config struct {
	File              string
	IndicatorFile     string
	FileIntegrityName string
	ConfigMapName     string
	Namespace         string
	Node              string
	Timeout           int64
	Interval          int64
	Compress          bool
}

type runtime struct {
	clientset  *kubernetes.Clientset
	dynclient  dynamic.Interface
	inode      uint64
	readoffset int64
}

var debugLog bool

func LOG(format string, a ...interface{}) {
	fmt.Printf(format+"\n", a...)
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
	cmd.Flags().String("file", "", "The log file to collect.")
	cmd.Flags().String("indicator-file", defaultIndicatorFile,
		"The path to a file containing a return code value. The return code value determines if the log file should be collected.")
	cmd.Flags().String("owner", "", "The FileIntegrity object to set as owner of the created configMap objects.")
	cmd.Flags().String("config-map-prefix", "", "Prefix for the configMap name, typically the podname.")
	cmd.Flags().String("namespace", "Running pod namespace.", ".")
	cmd.Flags().Int64("timeout", defaultTimeout, "How long to poll for the log and indicator files in seconds.")
	cmd.Flags().Int64("interval", defaultInterval, "How often to recheck for .")
	cmd.Flags().Bool("compress", false, "Use gzip+base64 to compress the log file contents.")
	cmd.Flags().Bool("debug", false, "Print debug messages")
}

func getConfigMapName(prefix, nodeName string) string {
	return prefix + "-" + nodeName
}

func parseConfig(cmd *cobra.Command) *config {
	var conf config
	conf.File = getValidStringArg(cmd, "file")
	conf.IndicatorFile = getValidStringArg(cmd, "indicator-file")
	conf.FileIntegrityName = getValidStringArg(cmd, "owner")
	conf.Namespace = getValidStringArg(cmd, "namespace")
	conf.Node = os.Getenv("NODE_NAME")
	conf.ConfigMapName = getConfigMapName(getValidStringArg(cmd, "config-map-prefix"), conf.Node)
	conf.Timeout, _ = cmd.Flags().GetInt64("timeout")
	conf.Interval, _ = cmd.Flags().GetInt64("interval")
	conf.Compress, _ = cmd.Flags().GetBool("compress")
	debugLog, _ = cmd.Flags().GetBool("debug")
	return &conf
}

func getValidStringArg(cmd *cobra.Command, name string) string {
	val, _ := cmd.Flags().GetString(name)
	if val == "" {
		FATAL("The command line argument '%s' is mandatory", name)
	}
	return val
}

func getFileIntegrityInstance(name, namespace string, dynclient dynamic.Interface) (*unstructured.Unstructured, error) {
	DBG("Getting FileIntegrity %s/%s", namespace, name)

	fiResource := schema.GroupVersionResource{
		Group:    crdGroup,
		Version:  crdAPIVersion,
		Resource: crdPlurals,
	}
	fi, err := dynclient.Resource(fiResource).Namespace(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return fi, nil
}

func waitForFile(filename string, timeout int64) (*os.File, uint64) {
	DBG("Waiting for %s", filename)

	readFileTimeoutChan := make(chan *os.File, 1)
	// G304 (CWE-22) is addressed by this.
	cleanFileName := filepath.Clean(filename)
	var inode uint64

	go func() {
		for {
			// Note that we're cleaning the filename path above.
			// #nosec
			file, err := os.Open(cleanFileName)
			if err == nil {
				fileinfo, err := file.Stat()
				sysfileinfointerface := fileinfo.Sys()
				sysfileinfo := sysfileinfointerface.(*syscall.Stat_t)
				inode = sysfileinfo.Ino
				// Only try to use the file if it already has contents.
				// This way we avoid race conditions between the side-car and
				// this script.
				if err == nil && fileinfo.Size() > 0 {
					readFileTimeoutChan <- file
				}
			} else if !os.IsNotExist(err) {
				FATAL("Error opening %s, %v", cleanFileName, err)
			}
			time.Sleep(1 * time.Second)
		}
	}()

	select {
	case file := <-readFileTimeoutChan:
		DBG("File '%s' found", filename)
		return file, inode
	case <-time.After(time.Duration(timeout) * time.Second):
		DBG("Timed out")
	}

	return nil, inode
}

func matchFileChangeRegex(contents []byte, regex string) string {
	re := regexp.MustCompile(regex)
	match := re.FindSubmatch(contents)
	if len(match) < 2 {
		return "0"
	}

	return string(match[1])
}

func annotateFileChangeSummary(contents []byte, annotations map[string]string) {
	annotations[common.IntegrityLogFilesAddedAnnotation] = matchFileChangeRegex(contents, `\s+Added files:\s+(?P<num_added>\d+)`)
	annotations[common.IntegrityLogFilesChangedAnnotation] = matchFileChangeRegex(contents, `\s+Changed files:\s+(?P<num_changed>\d+)`)
	annotations[common.IntegrityLogFilesRemovedAnnotation] = matchFileChangeRegex(contents, `\s+Removed files:\s+(?P<num_removed>\d+)`)
	DBG("added %s changed %s removed %s",
		annotations[common.IntegrityLogFilesAddedAnnotation],
		annotations[common.IntegrityLogFilesChangedAnnotation],
		annotations[common.IntegrityLogFilesRemovedAnnotation])
}

func needsCompression(contents []byte) bool {
	return len(contents) > uncompressedMaxSize // Magic number?
}

func compress(contents []byte) []byte {
	// Encode the contents ascii, compress it with gzip, b64encode it so it
	// can be stored in the configmap.
	var buffer bytes.Buffer
	w := gzip.NewWriter(&buffer)
	w.Write([]byte(contents))
	w.Close()
	return []byte(base64.StdEncoding.EncodeToString(buffer.Bytes()))
}

func getLogConfigMap(owner *unstructured.Unstructured, configMapName, contentkey, node string, contents []byte, compressed bool) *corev1.ConfigMap {
	annotations := map[string]string{}
	if compressed {
		annotations = map[string]string{
			common.CompressedLogsIndicatorLabelKey: "",
		}
	}

	annotateFileChangeSummary(contents, annotations)

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        configMapName,
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{
				metav1.OwnerReference{
					APIVersion: owner.GetAPIVersion(),
					Kind:       owner.GetKind(),
					Name:       owner.GetName(),
					UID:        owner.GetUID(),
				},
			},
			Labels: map[string]string{
				common.IntegrityConfigMapOwnerLabelKey: owner.GetName(),
				common.IntegrityLogLabelKey:            "",
				common.IntegrityConfigMapNodeLabelKey:  node,
			},
		},
		Data: map[string]string{
			contentkey: string(contents),
		},
	}
}

func getInformationalConfigMap(owner *unstructured.Unstructured, configMapName string, node string, annotations map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        configMapName,
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{
				metav1.OwnerReference{
					APIVersion: owner.GetAPIVersion(),
					Kind:       owner.GetKind(),
					Name:       owner.GetName(),
					UID:        owner.GetUID(),
				},
			},
			Labels: map[string]string{
				common.IntegrityConfigMapOwnerLabelKey: owner.GetName(),
				common.IntegrityLogLabelKey:            "",
				common.IntegrityConfigMapNodeLabelKey:  node,
			},
		},
	}
}

// reportOK creates a blank configMap with no error annotation. This is treated by the controller as an OK signal.
func reportOK(conf *config, rt *runtime) {
	DBG("Creating configMap '%s' to report OK", conf.ConfigMapName)
	err := backoff.Retry(func() error {
		fi, err := getFileIntegrityInstance(conf.FileIntegrityName, conf.Namespace, rt.dynclient)
		if err != nil {
			return err
		}
		confMap := getInformationalConfigMap(fi, conf.ConfigMapName, conf.Node, nil)
		_, err = rt.clientset.CoreV1().ConfigMaps(conf.Namespace).Create(confMap)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))

	if err != nil {
		FATAL("Can't create configMap to report OK: '%v', aborting", err)
	}
	LOG("Created OK configMap '%s'", conf.ConfigMapName)
}

func reportError(msg string, conf *config, rt *runtime) {
	DBG("Creating configMap '%s' to report error message '%s'", conf.ConfigMapName, msg)
	err := backoff.Retry(func() error {
		fi, err := getFileIntegrityInstance(conf.FileIntegrityName, conf.Namespace, rt.dynclient)
		if err != nil {
			return err
		}
		annotations := map[string]string{
			common.IntegrityLogErrorAnnotationKey: msg,
		}
		confMap := getInformationalConfigMap(fi, conf.ConfigMapName, conf.Node, annotations)
		_, err = rt.clientset.CoreV1().ConfigMaps(conf.Namespace).Create(confMap)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))

	if err != nil {
		FATAL("Can't create configMap to report failure '%v', aborting", err)
	}
	LOG("Created error configMap '%s'", conf.ConfigMapName)
}

func uploadLog(contents []byte, compressed bool, conf *config, rt *runtime) {
	DBG("Creating configMap '%s' to collect logs", conf.ConfigMapName)
	err := backoff.Retry(func() error {
		fi, err := getFileIntegrityInstance(conf.FileIntegrityName, conf.Namespace, rt.dynclient)
		if err != nil {
			return err
		}
		confMap := getLogConfigMap(fi, conf.ConfigMapName, common.IntegrityLogContentKey, conf.Node, contents, compressed)
		_, err = rt.clientset.CoreV1().ConfigMaps(conf.Namespace).Create(confMap)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))

	if err != nil {
		FATAL("Can't create log configMap with error '%v', aborting", err)
	}
	LOG("Created log configMap '%s'", conf.ConfigMapName)
}

func handleRotationOrInit(rt *runtime, inode uint64) {
	if rt.inode == 0 {
		// first read (set inode initially)
		rt.inode = inode
	} else if rt.inode != inode {
		DBG("Rotation happened")
		rt.readoffset = 0
	}
}

func updateReadOffset(rt *runtime, file *os.File, contents []byte) error {
	// We will start reading from the offset, so we'll read anything that's appended
	// to the file
	if _, err := file.Seek(rt.readoffset, 0); err != nil {
		// The might have file changed size (shrinked) since we calulated the
		// offset... Lets try to read from the beginning
		if _, err := file.Seek(0, 0); err != nil {
			return err
		}

		// reset the offset
		rt.readoffset = 0
	}
	rt.readoffset = rt.readoffset + int64(len(contents))
	return nil
}

func logCollectorMainLoop(cmd *cobra.Command, args []string) {
	conf := parseConfig(cmd)
	LOG("Starting the file-integrity log collector")

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

	rt := &runtime{
		clientset: clientset,
		dynclient: dynclient,
	}

	for {
		// Checking the indicator file for the last return code lets us determine if we should collect the aide log.
		indicatorFile, _ := waitForFile(conf.IndicatorFile, conf.Timeout)
		if indicatorFile == nil {
			DBG("Indicator file '%s' doesn't exist, trying again", conf.IndicatorFile)
			continue
		}

		// containsReturnCodeZero closes indicatorFile here.
		checkPassed, err := containsReturnCodeZero(indicatorFile)
		if err != nil {
			reportError(fmt.Sprintf("Return code file error: %s", err), conf, rt)
			continue
		}
		if checkPassed {
			DBG("Integrity check returned 0, sleeping and starting over")
			reportOK(conf, rt)
			time.Sleep(time.Duration(conf.Interval) * time.Second)
			continue
		}

		DBG("Integrity check failed, continuing to collect log file")
		file, inode := waitForFile(conf.File, conf.Timeout)

		handleRotationOrInit(rt, inode)

		contents := []byte{}
		if file != nil {
			if contents, err = ioutil.ReadAll(file); err != nil {
				reportError(fmt.Sprintf("Error reading the log file: %v", err), conf, rt)
				file.Close()
				continue
			}
			if err = updateReadOffset(rt, file, contents); err != nil {
				reportError(fmt.Sprintf("Error setting read offset for log file: %v", err), conf, rt)
				file.Close()
				continue
			}
			file.Close()
		}

		compressed := false
		if needsCompression(contents) || conf.Compress {
			DBG("Compressing log contents")
			contents = compress(contents)
			compressed = true
		}

		uploadLog(contents, compressed, conf, rt)
		DBG("Log uploaded, sleeping and starting over")
		time.Sleep(time.Duration(conf.Interval) * time.Second)
	}
}

// containsReturnCodeZero returns true, nil if the file contents starts with a single return code == 0, or err if there is
// a problem reading the file. Closes file.
func containsReturnCodeZero(file *os.File) (bool, error) {
	// Ignore warning about defer on file.Close().
	// #nosec
	defer file.Close()
	contents, err := ioutil.ReadAll(file)
	if err != nil {
		return false, err
	}

	retCode, err := strconv.Atoi(string(contents[0]))
	if err != nil {
		return false, err
	}

	if retCode == 0 {
		return true, nil
	}

	return false, nil
}
