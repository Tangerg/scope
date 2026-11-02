package runtest

import (
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func TestMemoryStoreLogConformance(t *testing.T) {
	RunStoreConformance(t, func(*testing.T) run.Store { return run.NewMemoryStore() })
}
