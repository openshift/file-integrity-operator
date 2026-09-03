package manager

import (
	"testing"

	"github.com/go-logr/logr/testr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	ctrl "sigs.k8s.io/controller-runtime"
)

func TestManager(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	RegisterFailHandler(Fail)
	RunSpecs(t, "Manager Suite")
}
