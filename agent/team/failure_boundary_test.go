package team

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	agentrun "github.com/Tangerg/scope/agent/run"
)

func TestFailuresPreserveStrategyClassification(t *testing.T) {
	for _, test := range []struct {
		name     string
		decision func(context.Context, Turn) (Decision, error)
		kind     agentrun.FailureKind
		code     string
	}{
		{"turn limit", func(_ context.Context, turn Turn) (Decision, error) {
			return Decision{Mode: ModeContinue, State: turn.State}, nil
		}, agentrun.FailureKindExecution, "collaboration.limit.turns"},
		{"invalid decision", func(_ context.Context, turn Turn) (Decision, error) {
			return Decision{Mode: ModeContinue, State: turn.State, Tasks: []TaskRequest{request("work", "absent", "x")}}, nil
		}, agentrun.FailureKindContract, "collaboration.decision.invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := fixtureConfig(test.decision, echo())
			config.MaxTurns = agentrun.NewQuota(1)
			definition := require(NewDefinition(config))
			_, process := run(t, definition, agentrun.NewMemoryStore())
			result := require(process.Await(t.Context()))
			if err := process.Join(t.Context()); err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if !failed || failure.Kind() != test.kind || failure.Code() != test.code {
				t.Fatalf("failure=%+v", failure)
			}
		})
	}
}

func TestSignalAdmissionAndZeroQuotaPolicies(t *testing.T) {
	config := fixtureConfig(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
	config.MaxTurns = agentrun.NewQuota(0)
	if _, err := NewDefinition(config); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	config.MaxTurns = agentrun.Quota{}
	config.MaxTasks = agentrun.NewQuota(0)
	definition := require(NewDefinition(config))
	conformancetest.Run(t, agentrun.BindingConfig{Definition: definition, ImplementationDigest: agentrun.ComputeDigest([]byte("admission")), ConfigurationDigest: agentrun.ComputeDigest([]byte("zero-tasks"))}, agentrun.Config{Store: agentrun.NewMemoryStore()}, input("initial"))
}
