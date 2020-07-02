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
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v3"
	"github.com/spf13/cobra"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openshift/file-integrity-operator/pkg/common"
)

const (
	crdGroup      = "fileintegrity.openshift.io"
	crdAPIVersion = "v1alpha1"
	crdPlurals    = "fileintegrities"
	maxRetries    = 5
	// These need to be lessened for normal use.
	defaultTimeout      = 600
	uncompressedMaxSize = 1048570 // 1MB for etcd limit
)

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

func getConfigMapName(prefix, nodeName string) string {
	return prefix + "-" + nodeName
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

func getNonEmptyFileAndInode(filename string) (*os.File, uint64) {
	DBG("Opening %s", filename)

	// G304 (CWE-22) is addressed by this.
	cleanFileName := filepath.Clean(filename)
	var inode uint64

	// Note that we're cleaning the filename path above.
	// #nosec
	file, err := os.Open(cleanFileName)
	if err != nil {
		LOG("error opening log file: %v", err)
		return nil, 0
	}

	fileinfo, err := file.Stat()
	if err != nil {
		return nil, 0
	}
	sysfileinfointerface := fileinfo.Sys()
	sysfileinfo := sysfileinfointerface.(*syscall.Stat_t)
	inode = sysfileinfo.Ino
	// Only try to use the file if it already has contents.
	if err == nil && fileinfo.Size() > 0 {
		return file, inode
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
func reportOK(conf *daemonConfig, rt *daemonRuntime) {
	err := backoff.Retry(func() error {
		fi, err := getFileIntegrityInstance(conf.LogCollectorFileIntegrityName, conf.LogCollectorNamespace, rt.dynclient)
		if err != nil {
			return err
		}
		confMap := getInformationalConfigMap(fi, conf.LogCollectorConfigMapName, conf.LogCollectorNode, nil)
		_, err = rt.clientset.CoreV1().ConfigMaps(conf.LogCollectorNamespace).Create(confMap)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))

	if err != nil {
		FATAL("Can't create configMap to report OK: '%v', aborting", err)
	}
	LOG("Created OK configMap '%s'", conf.LogCollectorConfigMapName)
}

func reportError(msg string, conf *daemonConfig, rt *daemonRuntime) {
	err := backoff.Retry(func() error {
		fi, err := getFileIntegrityInstance(conf.LogCollectorFileIntegrityName, conf.LogCollectorNamespace, rt.dynclient)
		if err != nil {
			return err
		}
		annotations := map[string]string{
			common.IntegrityLogErrorAnnotationKey: msg,
		}
		confMap := getInformationalConfigMap(fi, conf.LogCollectorConfigMapName, conf.LogCollectorNode, annotations)
		_, err = rt.clientset.CoreV1().ConfigMaps(conf.LogCollectorNamespace).Create(confMap)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))

	if err != nil {
		FATAL("Can't create configMap to report failure '%v', aborting", err)
	}
	LOG("Created error configMap '%s'", conf.LogCollectorConfigMapName)
}

func uploadLog(contents []byte, compressed bool, conf *daemonConfig, rt *daemonRuntime) {
	err := backoff.Retry(func() error {
		fi, err := getFileIntegrityInstance(conf.LogCollectorFileIntegrityName, conf.LogCollectorNamespace, rt.dynclient)
		if err != nil {
			return err
		}
		confMap := getLogConfigMap(fi, conf.LogCollectorConfigMapName, common.IntegrityLogContentKey, conf.LogCollectorNode, contents, compressed)
		_, err = rt.clientset.CoreV1().ConfigMaps(conf.LogCollectorNamespace).Create(confMap)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))

	if err != nil {
		FATAL("Can't create log configMap with error '%v', aborting", err)
	}
	LOG("Created log configMap '%s'", conf.LogCollectorConfigMapName)
}

func handleRotationOrInit(rt *daemonRuntime, inode uint64) {
	if rt.logCollectorInode == 0 {
		// first read (set inode initially)
		rt.logCollectorInode = inode
	} else if rt.logCollectorInode != inode {
		DBG("Rotation happened")
		rt.logCollectorReadoffset = 0
	}
}

func updateReadOffset(rt *daemonRuntime, file *os.File, contents []byte) error {
	// We will start reading from the offset, so we'll read anything that's appended
	// to the file
	if _, err := file.Seek(rt.logCollectorReadoffset, 0); err != nil {
		// The might have file changed size (shrinked) since we calulated the
		// offset... Lets try to read from the beginning
		if _, err := file.Seek(0, 0); err != nil {
			return err
		}

		// reset the offset
		rt.logCollectorReadoffset = 0
	}
	rt.logCollectorReadoffset = rt.logCollectorReadoffset + int64(len(contents))
	return nil
}

// logCollectorMainLoop creates temporary status report configMaps for the configmap controller to pick up and turn
// into permanent ones. It reads the last result reported from aide.
func logCollectorMainLoop(rt *daemonRuntime, conf *daemonConfig, ch chan bool) {
	for {
		lastResult := rt.Result()
		// We haven't received a result yet.
		if lastResult == -1 {
			DBG("No scan result available")
			time.Sleep(time.Duration(conf.Interval) * time.Second)
			continue
		}

		if lastResult == 0 {
			reportOK(conf, rt)
			time.Sleep(time.Duration(conf.Interval) * time.Second)
			continue
		}

		rt.LockAideFiles("logCollectorMainLoop")
		DBG("Integrity check failed, continuing to collect log file")
		file, inode := getNonEmptyFileAndInode(conf.LogCollectorFile)

		handleRotationOrInit(rt, inode)

		contents := []byte{}
		if file != nil {
			var err error
			if contents, err = ioutil.ReadAll(file); err != nil {
				reportError(fmt.Sprintf("Error reading the log file: %v", err), conf, rt)
				file.Close()
				rt.UnlockAideFiles("logCollectorMainLoop")
				continue
			}
			if err = updateReadOffset(rt, file, contents); err != nil {
				reportError(fmt.Sprintf("Error setting read offset for log file: %v", err), conf, rt)
				file.Close()
				rt.UnlockAideFiles("logCollectorMainLoop")
				continue
			}
			file.Close()
		}
		rt.UnlockAideFiles("logCollectorMainLoop")

		compressed := false
		if needsCompression(contents) || conf.LogCollectorCompress {
			DBG("Compressing log contents")
			contents = compress(contents)
			compressed = true
		}

		uploadLog(contents, compressed, conf, rt)
		time.Sleep(time.Duration(conf.Interval) * time.Second)
	}
}
