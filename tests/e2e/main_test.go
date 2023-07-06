package e2e

import (
	"os"
	"testing"

	"github.com/openshift/file-integrity-operator/tests/framework"
	log "github.com/sirupsen/logrus"
)

func TestMain(m *testing.M) {
	// This should setup a new test framework, which installs FIO into a
	// single namespace for all tests in the suite. The framework shouldn't
	// need to understand details about the golang test module (like
	// implementing runM).

	// Create framework
	f, err := framework.NewFramework()
	if err != nil {
		log.Fatalf("Failed to create test framework: %v", err)
	}

	// Use framework to setup namespace and operator
	f.SetUp()

	// Run tests
	exitCode := m.Run()

	// Use framework to tear down the operator and namespace
	if exitCode == 0 || (exitCode > 0 && !f.SkipCleanupOnError) {
		if err := f.TearDown(); err != nil {
			log.Fatal(err)
		}
	}

	os.Exit(exitCode)
}
