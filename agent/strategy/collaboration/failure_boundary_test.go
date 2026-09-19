package collaboration

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
)

func TestFailuresPreserveStrategyClassification(t *testing.T) {
	for _, test := range []struct {
		name     string
		decision func(context.Context, Turn) (Decision, error)
		kind     agent.FailureKind
		code     string
	}{
		{"turn limit", func(_ context.Context, turn Turn) (Decision, error) {
			return Decision{Mode: Continue, State: turn.State}, nil
		}, agent.FailureKindExecution, "collaboration.limit.turns"},
		{"invalid decision", func(_ context.Context, turn Turn) (Decision, error) {
			return Decision{Mode: Continue, State: turn.State, Tasks: []TaskRequest{request("work", "absent", "x")}}, nil
		}, agent.FailureKindContract, "collaboration.decision.invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, deployments := fixtureConfig(test.decision, echo())
			config.MaxTurns = agent.NewQuota(1)
			definition := require(NewDefinition(config))
			_, process := run(t, definition, deployments, nil)
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
	config, deployments := fixtureConfig(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
	config.MaxTurns = agent.NewQuota(0)
	if _, err := NewDefinition(config); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	config.MaxTurns = agent.Quota{}
	config.MaxTasks = agent.NewQuota(0)
	definition := require(NewDefinition(config))
	conformancetest.Run(t, agent.DeploymentConfig{Definition: definition, ImplementationDigest: agent.ComputeDigest([]byte("admission")), ConfigurationDigest: agent.ComputeDigest([]byte("zero-tasks"))}, agent.EngineConfig{DeploymentResolver: deployments}, input("initial"))
}
