package runtest_test

import (
	"testing"

	"github.com/Tangerg/scope/agent/run/runtest"

	"github.com/Tangerg/scope/agent/run"
)

func TestMemoryStoreConformance(t *testing.T) {
	runtest.RunTreeCommitterConformance(t, func() runtest.TreeCommitterConformanceDriver {
		return run.NewMemoryStore()
	})
}
