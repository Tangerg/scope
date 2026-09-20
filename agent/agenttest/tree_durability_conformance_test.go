package agenttest_test

import (
	"testing"

	"github.com/Tangerg/scope/agent/agenttest"

	"github.com/Tangerg/scope/agent"
)

func TestMemoryTreeCommitterConformance(t *testing.T) {
	agenttest.RunTreeCommitterConformance(t, func() agenttest.TreeCommitterConformanceDriver {
		return agent.NewMemoryTreeCommitter()
	})
}
