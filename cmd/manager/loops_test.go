package manager

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openshift/file-integrity-operator/pkg/common"
)

type daemonLoop func(context.Context, *daemonRuntime, *daemonConfig, chan<- error, *sync.WaitGroup)

// writeFakeAide puts an aide executable in dir. Initializing writes a database listing the files in dir, and a check
// is recorded in the scans file, takes scanSeconds and exits with scanExit.
func writeFakeAide(dir string, scanSeconds, scanExit int) {
	script := fmt.Sprintf(`#!/bin/sh
if [ "$3" = "-i" ]; then
	ls "%[1]s" > "%[1]s/%[2]s"
	exit 0
fi
echo scan >> "%[1]s/scans"
sleep %[3]d
exit %[4]d
`, dir, aideWritingDBFileName, scanSeconds, scanExit)
	Expect(os.WriteFile(path.Join(dir, "aide"), []byte(script), 0700)).To(Succeed())
}

func fileIntegrityWithAnnotations(annotations map[string]string) *unstructured.Unstructured {
	fi := &unstructured.Unstructured{}
	fi.SetAnnotations(annotations)
	return fi
}

var _ = Describe("AIDE daemon loops", func() {
	const nodeName = "test-node"
	var (
		dir     string
		conf    *daemonConfig
		rt      *daemonRuntime
		ctx     context.Context
		cancel  context.CancelFunc
		wg      sync.WaitGroup
		errChan chan error
		oldPath string
		oldNode string
	)

	inHoldoff := fileIntegrityWithAnnotations(map[string]string{common.IntegrityHoldoffAnnotationKey: nodeName})
	notInHoldoff := fileIntegrityWithAnnotations(nil)

	start := func(loops ...daemonLoop) {
		for _, loop := range loops {
			wg.Add(1)
			go loop(ctx, rt, conf, errChan, &wg)
		}
	}

	scans := func() int {
		data, err := os.ReadFile(path.Join(dir, "scans"))
		if err != nil {
			return 0
		}
		return strings.Count(string(data), "scan")
	}

	BeforeEach(func() {
		var err error
		dir, err = os.MkdirTemp("", "fio-daemon-loops-")
		Expect(err).ToNot(HaveOccurred())
		conf = &daemonConfig{FileDir: dir, RunDir: dir, ConfigDir: dir, Interval: 3600, MaxBackups: 5}
		rt = &daemonRuntime{}
		setInitialRuntimeState(rt)
		ctx, cancel = context.WithCancel(context.Background())
		errChan = make(chan error, 10)

		oldPath = os.Getenv("PATH")
		oldNode = os.Getenv("NODE_NAME")
		Expect(os.Setenv("PATH", dir+":"+oldPath)).To(Succeed())
		Expect(os.Setenv("NODE_NAME", nodeName)).To(Succeed())
		writeFakeAide(dir, 0, 0)
		// The node was already checked against this database before the update.
		Expect(os.WriteFile(aideReadDBPath(conf), []byte("db"), 0600)).To(Succeed())
	})

	AfterEach(func() {
		cancel()
		wg.Wait()
		Expect(errChan).ToNot(Receive())
		Expect(os.Setenv("PATH", oldPath)).To(Succeed())
		Expect(os.Setenv("NODE_NAME", oldNode)).To(Succeed())
		Expect(os.RemoveAll(dir)).To(Succeed())
	})

	Context("Scheduling the checks", func() {
		It("Runs the first check right away and the next ones every interval", func() {
			now := time.Now()
			Expect(scanDue(time.Time{}, time.Time{}, now, time.Hour)).To(BeTrue())
			Expect(scanDue(now.Add(-time.Minute), time.Time{}, now, time.Hour)).To(BeFalse())
			Expect(scanDue(now.Add(-time.Hour), time.Time{}, now, time.Hour)).To(BeTrue())
		})

		It("Runs a check right after a database initialization", func() {
			now := time.Now()
			Expect(scanDue(now.Add(-time.Minute), now.Add(-time.Second), now, time.Hour)).To(BeTrue())
			Expect(scanDue(now.Add(-time.Second), now.Add(-time.Minute), now, time.Hour)).To(BeFalse())
		})

		It("Holds the checks until the holdoff annotation is read", func() {
			Expect(scanAllowed(rt, conf)).To(BeFalse())
			rt.SetHolding("test", false)
			Expect(scanAllowed(rt, conf)).To(BeTrue())
		})

		It("Holds the checks while the node is held off or the database initializes", func() {
			rt.SetHolding("test", false)
			rt.SetInitializing("test", true)
			Expect(scanAllowed(rt, conf)).To(BeFalse())
			rt.SetInitializing("test", false)
			rt.SetHolding("test", true)
			Expect(scanAllowed(rt, conf)).To(BeFalse())
		})

		It("Holds the checks after a holdoff until the database is re-initialized", func() {
			Expect(createHoldoffFileIfMissing(conf)).To(Succeed())
			rt.SetHolding("test", false)
			Expect(scanAllowed(rt, conf)).To(BeFalse())

			Expect(handleAIDEInit(ctx, rt, conf, errChan, true)).To(Succeed())
			Expect(holdoffFileExists(conf)).To(BeFalse())
			Expect(rt.LastInit()).ToNot(BeZero())
			Expect(scanAllowed(rt, conf)).To(BeTrue())
		})

		It("Does not record the holdoff file in the re-initialized database", func() {
			Expect(createHoldoffFileIfMissing(conf)).To(Succeed())
			rt.SetHolding("test", false)
			Expect(handleAIDEInit(ctx, rt, conf, errChan, true)).To(Succeed())
			db, err := os.ReadFile(aideReadDBPath(conf))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(db)).ToNot(ContainSubstring(aideHoldoffFileName))
		})

		It("Does not initialize the database before the holdoff annotation is read", func() {
			Expect(os.WriteFile(aideReinitPath(conf), nil, 0600)).To(Succeed())
			start(reinitLoop)
			Consistently(rt.LastInit, 3*time.Second).Should(BeZero())

			rt.SetHolding("test", false)
			Eventually(rt.LastInit, 5*time.Second).ShouldNot(BeZero())
		})

		It("Does not end the holdoff with a database initialized during it", func() {
			Expect(createHoldoffFileIfMissing(conf)).To(Succeed())
			rt.SetHolding("test", true)
			Expect(handleAIDEInit(ctx, rt, conf, errChan, true)).To(Succeed())
			Expect(holdoffFileExists(conf)).To(BeTrue())

			rt.SetHolding("test", false)
			Expect(scanAllowed(rt, conf)).To(BeFalse())
		})

		It("Resumes the checks if no re-init follows the holdoff", func() {
			Expect(createHoldoffFileIfMissing(conf)).To(Succeed())
			rt.SetHolding("test", false)
			rt.notHoldingSince = time.Now().Add(-holdoffReinitTimeout)
			Expect(scanAllowed(rt, conf)).To(BeTrue())
			Expect(holdoffFileExists(conf)).To(BeFalse())
		})
	})

	Context("Holding off the checks", func() {
		It("Records the holdoff on the host until the database is re-initialized", func() {
			rt.SetFileIntegrityInstance(inHoldoff)
			start(holdOffLoop)
			Eventually(func() bool { return holdoffFileExists(conf) }, 5*time.Second).Should(BeTrue())
			Expect(rt.Holding()).To(BeTrue())

			rt.SetFileIntegrityInstance(notInHoldoff)
			Eventually(rt.Holding, 5*time.Second).Should(BeFalse())
			Expect(holdoffFileExists(conf)).To(BeTrue())
		})

		It("Ignores the holdoff of other nodes", func() {
			rt.SetFileIntegrityInstance(fileIntegrityWithAnnotations(map[string]string{
				common.IntegrityHoldoffAnnotationKey: "other-node",
			}))
			start(holdOffLoop)
			Eventually(rt.Holding).Should(BeFalse())
			Expect(holdoffFileExists(conf)).To(BeFalse())
		})
	})

	Context("Running the checks around a node update", func() {
		It("Does not check before the holdoff annotation is read", func() {
			start(aideLoop)
			Consistently(scans, 3*time.Second).Should(BeZero())
		})

		It("Does not check a node that is held off when the daemon starts", func() {
			rt.SetFileIntegrityInstance(inHoldoff)
			start(holdOffLoop, aideLoop)
			Consistently(scans, 3*time.Second).Should(BeZero())
		})

		It("Waits for the re-init after the update, then checks right away", func() {
			rt.SetFileIntegrityInstance(inHoldoff)
			start(holdOffLoop, reinitLoop, aideLoop)
			Consistently(scans, 2*time.Second).Should(BeZero())

			By("finishing the update, the re-init daemonSet has not run yet")
			rt.SetFileIntegrityInstance(notInHoldoff)
			Eventually(rt.Holding, 5*time.Second).Should(BeFalse())
			Consistently(scans, 3*time.Second).Should(BeZero())

			By("running the re-init daemonSet")
			Expect(os.WriteFile(aideReinitPath(conf), nil, 0600)).To(Succeed())
			Eventually(rt.GetResult(), 10*time.Second).Should(Receive(Equal(0)))
			Expect(scans()).To(Equal(1))
		})

		It("Discards the result of a check that a node update started during", func() {
			writeFakeAide(dir, 3, 7)
			rt.SetFileIntegrityInstance(notInHoldoff)
			start(holdOffLoop, aideLoop)
			Eventually(scans, 5*time.Second).Should(Equal(1))

			rt.SetFileIntegrityInstance(inHoldoff)
			Consistently(rt.GetResult(), 5*time.Second).ShouldNot(Receive())
		})
	})
})
