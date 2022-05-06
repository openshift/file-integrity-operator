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
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"regexp"
	"time"

	"github.com/cenkalti/backoff/v4"
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
	defaultSleep     = 5 * time.Second
	configMapMaxSize = 1048570 // 1MB for etcd limit. Over this, you get an error.
)

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

func compress(in []byte) ([]byte, error) {
	// Encode the contents ascii, compress it with gzip, b64encode it so it
	// can be stored in the configmap.
	var buffer bytes.Buffer

	w := gzip.NewWriter(&buffer)
	if _, cpyErr := io.Copy(w, bytes.NewReader(in)); cpyErr != nil {
		if err := w.Close(); err != nil {
			return nil, err
		}
		return nil, cpyErr
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
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

func newLogConfigMap(owner *unstructured.Unstructured, configMapName, contentkey, node string, contents, compressedContents []byte) *corev1.ConfigMap {
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

func newInformationalConfigMap(owner *unstructured.Unstructured, configMapName string, node string, annotations map[string]string) *corev1.ConfigMap {
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
	}
}

// reportOK creates a blank configMap with no error annotation. This is treated by the controller as an OK signal.
func reportOK(ctx context.Context, conf *daemonConfig, rt *daemonRuntime) error {
	DBG("creating temporary configMap '%s' to report a successful scan result", conf.LogCollectorConfigMapName)
	return backoff.Retry(func() error {
		fi := rt.GetFileIntegrityInstance()
		confMap := newInformationalConfigMap(fi, conf.LogCollectorConfigMapName, conf.LogCollectorNode, nil)
		_, err := rt.clientset.CoreV1().ConfigMaps(conf.Namespace).Create(ctx, confMap, metav1.CreateOptions{})
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))
}

func reportError(ctx context.Context, msg string, conf *daemonConfig, rt *daemonRuntime) error {
	DBG("creating temporary configMap '%s' to report an ERROR scan result", conf.LogCollectorConfigMapName)
	return backoff.Retry(func() error {
		fi := rt.GetFileIntegrityInstance()
		annotations := map[string]string{
			common.IntegrityLogErrorAnnotationKey: msg,
		}
		confMap := newInformationalConfigMap(fi, conf.LogCollectorConfigMapName, conf.LogCollectorNode, annotations)
		_, err := rt.clientset.CoreV1().ConfigMaps(conf.Namespace).Create(ctx, confMap, metav1.CreateOptions{})
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))
}

func uploadLog(ctx context.Context, contents, compressedContents []byte, conf *daemonConfig, rt *daemonRuntime) error {
	DBG("creating temporary configMap '%s' to report a FAILED scan result", conf.LogCollectorConfigMapName)
	return backoff.Retry(func() error {
		fi := rt.GetFileIntegrityInstance()
		confMap := newLogConfigMap(fi, conf.LogCollectorConfigMapName, common.IntegrityLogContentKey, conf.LogCollectorNode, contents, compressedContents)
		_, err := rt.clientset.CoreV1().ConfigMaps(conf.Namespace).Create(ctx, confMap, metav1.CreateOptions{})
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))
}
