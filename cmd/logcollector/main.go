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
)

type config struct {
	File              string
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

func defineFlags(cmd *cobra.Command) {
	cmd.Flags().String("file", "", "The file to watch.")
	cmd.Flags().String("owner", "", "The compliance scan that owns the configMap objects.")
	cmd.Flags().String("config-map-prefix", "", "The configMap prefix to upload, typically the podname.")
	cmd.Flags().String("namespace", "Running pod namespace.", ".")
	cmd.Flags().Int64("timeout", 600, "How long to wait for the file.")
	cmd.Flags().Int64("interval", 1800, "every how often does the log collector run.")
	cmd.Flags().Bool("compress", false, "Always compress the results.")
}

func getConfigMapName(prefix, nodeName string) string {
	return prefix + "-" + nodeName
}

func parseConfig(cmd *cobra.Command) *config {
	var conf config
	conf.File = getValidStringArg(cmd, "file")
	conf.FileIntegrityName = getValidStringArg(cmd, "owner")
	conf.Namespace = getValidStringArg(cmd, "namespace")
	conf.Node = os.Getenv("NODE_NAME")
	conf.ConfigMapName = getConfigMapName(getValidStringArg(cmd, "config-map-prefix"), conf.Node)
	conf.Timeout, _ = cmd.Flags().GetInt64("timeout")
	conf.Interval, _ = cmd.Flags().GetInt64("interval")
	conf.Compress, _ = cmd.Flags().GetBool("compress")
	return &conf
}

func getValidStringArg(cmd *cobra.Command, name string) string {
	val, _ := cmd.Flags().GetString(name)
	if val == "" {
		fmt.Fprintf(os.Stderr, "The command line argument '%s' is mandatory.\n", name)
		os.Exit(1)
	}
	return val
}

func getFileIntegrityInstance(name, namespace string, dynclient dynamic.Interface) (*unstructured.Unstructured, error) {
	fiResource := schema.GroupVersionResource{
		Group:    crdGroup,
		Version:  crdAPIVersion,
		Resource: crdPlurals,
	}

	fmt.Printf("Getting FileIntegrity %s/%s\n", namespace, name)
	fi, err := dynclient.Resource(fiResource).Namespace(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	return fi, nil
}

func waitForFile(filename string, timeout int64) (*os.File, uint64) {
	fmt.Printf("Waiting for %s.\n", filename)
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
				fmt.Println(err)
				os.Exit(1)
			}
			time.Sleep(1 * time.Second)
		}
	}()

	select {
	case file := <-readFileTimeoutChan:
		fmt.Printf("File '%s' found, will upload it.\n", filename)
		return file, inode
	case <-time.After(time.Duration(timeout) * time.Second):
		fmt.Println("Timeout. The integrity check hasn't reported anything. This is good!")
	}

	return nil, inode
}

func needsCompression(contents []byte) bool {
	return len(contents) > 1048570
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

func reportError(msg string, conf *config, rt *runtime) {
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
		fmt.Println(err)
		fmt.Println("Can't report the failure by creating a config map... Aborting")
		os.Exit(1)
	}
}

func uploadLog(contents []byte, compressed bool, conf *config, rt *runtime) {
	err := backoff.Retry(func() error {
		fi, err := getFileIntegrityInstance(conf.FileIntegrityName, conf.Namespace, rt.dynclient)
		if err != nil {
			return err
		}
		confMap := getLogConfigMap(fi, conf.ConfigMapName, common.IntegrityLogContentKey, conf.Node, contents, compressed)
		fmt.Println("Creating configmap")
		_, err = rt.clientset.CoreV1().ConfigMaps(conf.Namespace).Create(confMap)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), maxRetries))

	if err != nil {
		fmt.Println(err)
		fmt.Println("Can't create the log config map... Aborting")
		os.Exit(1)
	}
}

func handleRotationOrInit(rt *runtime, inode uint64) {
	if rt.inode == 0 {
		// first read (set inode initially)
		rt.inode = inode
	} else if rt.inode != inode {
		// Rotation has happened
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

func doMainLoop(cmd *cobra.Command, args []string) {
	conf := parseConfig(cmd)
	fmt.Println("Starting log collector.")

	config, err := rest.InClusterConfig()
	if err != nil {
		fmt.Println(err)
		// Fatal error
		os.Exit(1)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Println(err)
		// Fatal error
		os.Exit(1)
	}
	dynclient, err := dynamic.NewForConfig(config)
	if err != nil {
		fmt.Println(err)
		// Fatal error
		os.Exit(1)
	}

	rt := &runtime{
		clientset: clientset,
		dynclient: dynclient,
	}

	for {
		file, inode := waitForFile(conf.File, conf.Timeout)

		handleRotationOrInit(rt, inode)

		contents := []byte{}
		if file != nil {
			if contents, err = ioutil.ReadAll(file); err != nil {
				reportError("Couldn't read the log file", conf, rt)
				file.Close()
				continue
			}
			if err = updateReadOffset(rt, file, contents); err != nil {
				reportError("Error setting read offset for log file", conf, rt)
				file.Close()
				continue
			}
			file.Close()
		}

		compressed := false
		if needsCompression(contents) || conf.Compress {
			contents = compress(contents)
			compressed = true
			fmt.Println("Needs compression.")
		}

		uploadLog(contents, compressed, conf, rt)
		time.Sleep(time.Duration(conf.Interval) * time.Second)
	}
}

func main() {
	var rootCmd = &cobra.Command{
		Use:   "file-integrity-logcollector",
		Short: "A tool that gets the results of the file integrity checks.",
		Long:  "A tool that gets the results of the file integrity checks.",
		Run:   doMainLoop,
	}

	defineFlags(rootCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
