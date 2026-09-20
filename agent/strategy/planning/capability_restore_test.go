package planning_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/planning"
)

func TestRestoredPlanningEffectsCannotDropBindingCapabilities(t *testing.T) {
	for _, omitRequired := range []bool{false, true} {
		name := "required capability retained"
		if omitRequired {
			name = "required capability omitted"
		}
		t.Run(name, func(t *testing.T) {
			done := mustCondition(t, "world.done", planning.True)
			action := mustAction(t, planning.ActionConfig{
				Name: "action.finish", Description: "Finish the work.", Effects: []planning.Condition{done},
			})
			capability, err := agent.ParseCapability("planning.finish")
			if err != nil {
				t.Fatal(err)
			}
			grant, err := agent.NewCapabilitySet(capability)
			if err != nil {
				t.Fatal(err)
			}
			binding, err := planning.NewDispatcherBinding(planning.DispatcherBindingConfig{
				Action: action, RequiredCapabilities: []agent.Capability{capability},
			})
			if err != nil {
				t.Fatal(err)
			}
			world := newManagedWorld(t)
			deployment := newManagedDeployment(t, managedDeploymentConfig{
				goal: mustGoal(t, done), bindings: []planning.ActionBinding{binding}, sensor: world,
				executors: map[string]planning.ActionExecutor{action.Name(): world.apply(action)},
			})
			interrupted := &planningActionBoundary{
				TreeCommitter: agent.NewMemoryTreeCommitter(), cause: errors.New("stop before Action dispatch"),
			}
			source, err := agent.NewEngine(agent.EngineConfig{Capabilities: grant, TreeCommitter: interrupted})
			if err != nil {
				t.Fatal(err)
			}
			input, err := agent.EncodePayload(struct{}{})
			if err != nil {
				t.Fatal(err)
			}
			if _, runErr := source.Run(t.Context(), deployment, input); !errors.Is(runErr, interrupted.cause) {
				t.Fatalf("source Run = %v, want interrupted Action boundary", runErr)
			}
			if closeErr := source.Close(context.WithoutCancel(t.Context())); closeErr != nil {
				t.Fatal(closeErr)
			}
			var wire map[string]any
			decoder := json.NewDecoder(bytes.NewReader(interrupted.snapshot.JSON()))
			decoder.UseNumber()
			if decodeErr := decoder.Decode(&wire); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			processWire := wire["process_snapshots"].([]any)[0].(map[string]any)
			prepared := processWire["prepared"].(map[string]any)
			record := prepared["effects"].([]any)[0].(map[string]any)
			record["phase"] = "planned"
			if omitRequired {
				processWire["capabilities"] = []any{}
				delete(record["effect"].(map[string]any), "required_capabilities")
			}
			snapshot, err := agent.ParseTreeSnapshot(mustJSON(t, wire))
			if err != nil {
				t.Fatal(err)
			}
			events := &agenttest.ObservationRecorder{}
			engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: &planningSnapshotCommitter{head: snapshot}, EventListeners: []agent.EventListener{events}})
			if err != nil {
				t.Fatal(err)
			}
			process, err := engine.RestoreTree(t.Context(), deployment, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if killErr := process.Kill(ctx, "release capability test tree"); killErr != nil && !errors.Is(killErr, agent.ErrProcessFinished) {
					t.Error(killErr)
				}
				if releaseErr := engine.ReleaseTree(ctx, process.ID()); releaseErr != nil {
					t.Error(releaseErr)
				}
				if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if !omitRequired {
				result, awaitErr := process.Await(ctx)
				if awaitErr != nil {
					t.Fatal(awaitErr)
				}
				if output := managedOutput(t, result); output.Outcome != planning.OutcomeAchieved {
					t.Fatalf("granted Action outcome = %s", output.Outcome)
				}
				return
			}
			settled, err := events.AwaitEvent(ctx, func(event agent.Event) bool {
				sequence, present := event.StepSequence()
				return event.Name() == agent.EventEffectFinished && present && sequence == interrupted.step
			})
			if err != nil {
				t.Fatal(err)
			}
			fact, present := settled.EffectFinished()
			if !present || fact.SettlementStatus() != agent.SettlementStatusFailed {
				t.Errorf("invalid Action settlement = %s, want failed", fact.SettlementStatus())
			}
			result, awaitErr := process.Await(ctx)
			failure, failed := result.Termination().Failure()
			if awaitErr != nil || result.Status() != agent.StatusFailed || !failed || failure.Code() != "planning.dispatch.rejected" || len(result.Termination().UnresolvedEffectIDs()) != 0 {
				t.Fatalf("local rejection result = %+v, failure = %+v, error = %v", result, failure, awaitErr)
			}
			if world.truth("world.done") != planning.Unknown {
				t.Error("restored Effect dropped its required capability and reached the Action executor")
			}
		})
	}
}

type planningActionBoundary struct {
	agent.TreeCommitter
	snapshot agent.TreeSnapshot
	step     uint64
	cause    error
}

func (p *planningActionBoundary) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	if boundary.Kind() == agent.EffectBoundaryKindPending && len(boundary.Request().Effect().RequiredCapabilities().Values()) > 0 {
		p.snapshot = boundary.TreeSnapshot()
		p.step = boundary.Request().StepSequence()
		return p.cause
	}
	return p.TreeCommitter.CommitEffect(ctx, boundary)
}

// This fixture represents a corrupted persistent image. It still fences each
// subsequent writer and head update; the restored binding must reject the
// damaged capability grant before an action executor can be reached.
type planningSnapshotCommitter struct{ head agent.TreeSnapshot }

func (p *planningSnapshotCommitter) ActivateTree(_ context.Context, activation agent.TreeActivation) error {
	if p.head.Digest() != activation.PreviousTreeDigest() || p.head.IncarnationID() != activation.PreviousIncarnationID() {
		return agent.ErrTreeIncarnationConflict
	}
	p.head = activation.TreeSnapshot()
	return nil
}
func (p *planningSnapshotCommitter) advance(previous agent.Digest, snapshot agent.TreeSnapshot) error {
	if p.head.Digest() != previous || p.head.IncarnationID() != snapshot.IncarnationID() {
		return agent.ErrTreeIncarnationConflict
	}
	p.head = snapshot
	return nil
}
func (p *planningSnapshotCommitter) CommitEffect(_ context.Context, boundary agent.EffectBoundary) error {
	return p.advance(boundary.PreviousTreeDigest(), boundary.TreeSnapshot())
}
func (p *planningSnapshotCommitter) CommitCheckpoint(_ context.Context, checkpoint agent.TreeCheckpoint) error {
	return p.advance(checkpoint.PreviousTreeDigest(), checkpoint.TreeSnapshot())
}
