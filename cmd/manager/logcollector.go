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
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/cenkalti/backoff/v3"
	"github.com/spf13/cobra"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openshift/file-integrity-operator/pkg/common"
)

const (
	crdGroup      = "fileintegrity.openshift.io"
	crdAPIVersion = "v1alpha1"
	crdPlurals    = "fileintegrities"
	maxRetries    = 5
	// These need to be lessened for normal use.
	defaultTimeout   = 600
	configMapMaxSize = 1048570 // 1MB for etcd limit. Over this, you get an error.
)

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

func matchFileChangeRegex(contents string, regex string) string {
	re := regexp.MustCompile(regex)
	match := re.FindStringSubmatch(contents)
	if len(match) < 2 {
		return "0"
	}

	return string(match[1])
}

func annotateFileChangeSummary(contents string, annotations map[string]string) {
	annotations[common.IntegrityLogFilesAddedAnnotation] = matchFileChangeRegex(contents, `\s+Added entries:\s+(?P<num_added>\d+)`)
	annotations[common.IntegrityLogFilesChangedAnnotation] = matchFileChangeRegex(contents, `\s+Changed entries:\s+(?P<num_changed>\d+)`)
	annotations[common.IntegrityLogFilesRemovedAnnotation] = matchFileChangeRegex(contents, `\s+Removed entries:\s+(?P<num_removed>\d+)`)
	DBG("added %s changed %s removed %s",
		annotations[common.IntegrityLogFilesAddedAnnotation],
		annotations[common.IntegrityLogFilesChangedAnnotation],
		annotations[common.IntegrityLogFilesRemovedAnnotation])
}

func needsCompression(size int64) bool {
	return size > configMapMaxSize
}

func compress(in []byte) []byte {
	// Encode the contents ascii, compress it with gzip, b64encode it so it
	// can be stored in the configmap.
	var buffer bytes.Buffer

	w := gzip.NewWriter(&buffer)
	io.Copy(w, bytes.NewReader(in))
	w.Close()
	return buffer.Bytes()
}

func encodetoBase64(src []byte) string {
	r := bytes.NewReader(src)
	pr, pw := io.Pipe()
	enc := base64.NewEncoder(base64.StdEncoding, pw)
	go func() {
		_, err := io.Copy(enc, r)
		enc.Close()

		if err != nil {
			pw.CloseWithError(err)
		} else {
			pw.Close()
		}
	}()
	out, _ := ioutil.ReadAll(pr)
	return string(out)
}

func getLogConfigMap(owner *unstructured.Unstructured, configMapName, contentkey, node string, contents, compressedContents []byte) *corev1.ConfigMap {
	annotations := map[string]string{}

	strcontents := string(contents)
	DBG("uncompressed log size: %d", len(strcontents))
	annotateFileChangeSummary(strcontents, annotations)

	if size := len(compressedContents); size > 0 {
		if size > configMapMaxSize {
			DBG("compressed AIDE log is too large (%d), max allowed is %d.", size, configMapMaxSize)
			strcontents = fmt.Sprintf(
				"compressed AIDE log is too large for a configMap (%d) - fetch it from /etc/kubernetes/aide.log on node %s",
				size, node)
		} else {
			strcontents = encodetoBase64(compressedContents)
			DBG("compressed, encoded log size: %d", len(strcontents))
		}
		annotations[common.CompressedLogsIndicatorLabelKey] = ""
	}

	// Check again, because the base64 encoding could push it over the limit, if the compressed log size was right
	// under the limit.
	if size := len(strcontents); size > configMapMaxSize {
		DBG("compressed, encoded AIDE log is too large (%d), max allowed is %d.", size, configMapMaxSize)
		strcontents = fmt.Sprintf(
			"compressed, encoded AIDE log is too large for a configMap (%d) - fetch it from /etc/kubernetes/aide.log on node %s",
			size, node)
	}

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        configMapName,
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: owner.GetAPIVersion(),
					Kind:       owner.GetKind(),
					Name:       owner.GetName(),
					UID:        owner.GetUID(),
				},
			},
			Labels: map[string]string{
				common.IntegrityOwnerLabelKey:         owner.GetName(),
				common.IntegrityLogLabelKey:           "",
				common.IntegrityConfigMapNodeLabelKey: node,
			},
		},
		Data: map[string]string{
			contentkey: strcontents,
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
				common.IntegrityOwnerLabelKey:         owner.GetName(),
				common.IntegrityLogLabelKey:           "",
				common.IntegrityConfigMapNodeLabelKey: node,
			},
		},
	}
}

// reportOK creates a blank configMap with no error annotation. This is treated by the controller as an OK signal.
func reportOK(conf *daemonConfig, rt *daemonRuntime) {
	err := backoff.Retry(func() error {
		fi := rt.GetFileIntegrityInstance()
		confMap := getInformationalConfigMap(fi, conf.LogCollectorConfigMapName, conf.LogCollectorNode, nil)
		_, err := rt.clientset.CoreV1().ConfigMaps(conf.Namespace).Create(context.TODO(), confMap, metav1.CreateOptions{})
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))

	if err != nil {
		FATAL("Can't create configMap to report a successful scan result: '%v', aborting", err)
	}
	DBG("Created temporary configMap '%s' to report a successful scan result", conf.LogCollectorConfigMapName)
}

func reportError(msg string, conf *daemonConfig, rt *daemonRuntime) {
	err := backoff.Retry(func() error {
		fi := rt.GetFileIntegrityInstance()
		annotations := map[string]string{
			common.IntegrityLogErrorAnnotationKey: msg,
		}
		confMap := getInformationalConfigMap(fi, conf.LogCollectorConfigMapName, conf.LogCollectorNode, annotations)
		_, err := rt.clientset.CoreV1().ConfigMaps(conf.Namespace).Create(context.TODO(), confMap, metav1.CreateOptions{})
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))

	if err != nil {
		FATAL("Can't create configMap to report an ERROR scan result: '%v', aborting", err)
	}
	LOG("Created temporary configMap '%s' to report an 'ERROR' scan result", conf.LogCollectorConfigMapName)
}

func uploadLog(contents, compressedContents []byte, conf *daemonConfig, rt *daemonRuntime) {
	err := backoff.Retry(func() error {
		fi := rt.GetFileIntegrityInstance()
		confMap := getLogConfigMap(fi, conf.LogCollectorConfigMapName, common.IntegrityLogContentKey, conf.LogCollectorNode, contents, compressedContents)
		_, err := rt.clientset.CoreV1().ConfigMaps(conf.Namespace).Create(context.TODO(), confMap, metav1.CreateOptions{})
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))

	if err != nil {
		FATAL("Can't create log configMap with error '%v', aborting", err)
	}
	LOG("Created log configMap '%s' to report a failed scan result", conf.LogCollectorConfigMapName)
}

// logCollectorMainLoop creates temporary status report configMaps for the configmap controller to pick up and turn
// into permanent ones. It reads the last result reported from aide.
func logCollectorMainLoop(rt *daemonRuntime, conf *daemonConfig) {
	for {
		lastResult := <-rt.result
		// We haven't received a result yet.
		// Or return code 18 - AIDE ran prior to there being an aide database.
		if lastResult == -1 || lastResult == 18 {
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

		if file := getNonEmptyFile(conf.LogCollectorFile); file != nil {
			var compressedContents []byte

			fileinfo, err := file.Stat()
			if err != nil {
				reportError(fmt.Sprintf("Error getting file information: %v", err), conf, rt)
				file.Close()
				rt.UnlockAideFiles("logCollectorMainLoop")
				continue
			}

			fileSize := fileinfo.Size()
			// Always read in the contents, when compressed we still need the uncompressed version to figure out
			// the changed details when updating the configMap later on.
			r := bufio.NewReader(file)
			contents, err := ioutil.ReadAll(r)
			if err != nil {
				reportError(fmt.Sprintf("Error reading file: %v", err), conf, rt)
				file.Close()
				rt.UnlockAideFiles("logCollectorMainLoop")
				continue
			}
			file.Close()

			if needsCompression(fileSize) || conf.LogCollectorCompress {
				DBG("Compressing log contents")
				compressedContents = compress(contents)
			}

			uploadLog(contents, compressedContents, conf, rt)
		}

		rt.UnlockAideFiles("logCollectorMainLoop")
	}
}
