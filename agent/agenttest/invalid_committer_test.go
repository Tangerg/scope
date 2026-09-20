package agenttest_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
)

// The suite itself needs negative coverage: a passing reference implementation
// cannot establish that a missing storage guarantee is detected.
func TestTreeCommitterConformanceRejectsBrokenStores(t *testing.T) {
	const fixtureEnv = "SCOPE_INVALID_COMMITTER_FIXTURE"
	if fixture := os.Getenv(fixtureEnv); fixture != "" {
		agenttest.RunTreeCommitterConformance(t, func() agenttest.TreeCommitterConformanceDriver {
			return &invalidCommitter{MemoryTreeCommitter: agent.NewMemoryTreeCommitter(), fixture: fixture}
		})
		return
	}
	for _, test := range []struct{ name, scenario, diagnostic string }{
		{"missing_head", "effect_boundaries_and_terminal_head", "authoritative terminal head exists=false"},
		{"accepted_conflict", "effect_boundaries_and_terminal_head", "duplicate error=<nil>, want ErrCommitConflict"},
		{"missing_cas", "concurrent_restore_fencing", "restore winner=true conflicts=0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestTreeCommitterConformanceRejectsBrokenStores$/^"+test.scenario+"$", "-test.timeout=15s")
			command.Env = append(os.Environ(), fixtureEnv+"="+test.name)
			output, err := command.CombinedOutput()
			var failure *exec.ExitError
			if !errors.As(err, &failure) || failure.ExitCode() != 1 || !strings.Contains(string(output), test.diagnostic) {
				t.Fatalf("invalid store was not rejected by the expected contract: %v\n%s", err, output)
			}
		})
	}
}

type invalidCommitter struct {
	*agent.MemoryTreeCommitter
	fixture string
}

func (i *invalidCommitter) LoadTree(ctx context.Context, rootID agent.ProcessID) (agent.TreeSnapshot, bool, error) {
	if i.fixture == "missing_head" {
		return agent.TreeSnapshot{}, false, nil
	}
	return i.MemoryTreeCommitter.LoadTree(ctx, rootID)
}

func (i *invalidCommitter) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	err := i.MemoryTreeCommitter.CommitEffect(ctx, boundary)
	if i.fixture == "accepted_conflict" && errors.Is(err, agent.ErrCommitConflict) {
		return nil
	}
	return err
}

func (i *invalidCommitter) ActivateTree(ctx context.Context, activation agent.TreeActivation) error {
	err := i.MemoryTreeCommitter.ActivateTree(ctx, activation)
	if i.fixture == "missing_cas" && errors.Is(err, agent.ErrTreeIncarnationConflict) {
		return nil
	}
	return err
}
