package planning_test

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/metadata"

	"github.com/Tangerg/scope/agent/planning"
	"github.com/Tangerg/scope/agent/run"
)

func TestRestoredPlanningActionRequiresBindingCapabilities(t *testing.T) {
	for _, omitRequired := range []bool{false, true} {
		name := "capability retained"
		if omitRequired {
			name = "capability dropped from the restored Process"
		}
		t.Run(name, func(t *testing.T) {
			done := mustCondition(t, "world.done", planning.TruthTrue)
			action := mustAction(t, planning.ActionConfig{
				Name: "action.finish", Description: "Finish the work.", Effects: []planning.Condition{done},
			})
			capability, err := run.ParseCapability("planning.finish")
			if err != nil {
				t.Fatal(err)
			}
			grant, err := run.NewCapabilitySet(capability)
			if err != nil {
				t.Fatal(err)
			}
			binding, err := planning.NewDispatcherBinding(planning.DispatcherBindingConfig{
				Action: action, RequiredCapabilities: []run.Capability{capability},
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
				Store: run.NewMemoryStore(), cause: errors.New("stop before Action dispatch"),
			}
			source, err := run.New(t.Context(), run.Config{Capabilities: grant, Store: interrupted})
			if err != nil {
				t.Fatal(err)
			}
			input, err := run.EncodePayload(struct{}{})
			if err != nil {
				t.Fatal(err)
			}
			if _, runErr := runtest.Observe(t.Context(), source, deployment, input); !errors.Is(runErr, interrupted.cause) {
				t.Fatalf("source Run = %v, want interrupted Action boundary", runErr)
			}
			if closeErr := source.Close(context.WithoutCancel(t.Context())); closeErr != nil {
				t.Fatal(closeErr)
			}
			var wire metadata.Map
			if decodeErr := jsonv2.Unmarshal(interrupted.snapshot.JSON(), &wire); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			wireValues, err := wire.Values()
			if err != nil {
				t.Fatal(err)
			}
			processWire := wireValues["process_snapshots"].([]any)[0].(map[string]any)
			prepared := processWire["prepared"].(map[string]any)
			record := prepared["effects"].([]any)[0].(map[string]any)
			delete(record, "progress")
			if omitRequired {
				processWire["capabilities"] = []any{}
			}
			snapshot, err := run.ParseTreeSnapshot(mustJSON(t, wireValues))
			if err != nil {
				t.Fatal(err)
			}
			engine, err := run.New(t.Context(), run.Config{Store: &planningSnapshotCommitter{head: snapshot}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			process, err := engine.Recover(t.Context(), deployment, snapshot)
			if omitRequired {
				if !errors.Is(err, run.ErrInvalidSnapshot) || !errors.Is(err, run.ErrInvalidCapability) {
					t.Fatalf("restore without the binding capability = %v, want invalid capability snapshot", err)
				}
				if world.truth("world.done") != planning.TruthUnknown {
					t.Error("restored Action without its capability reached the Action executor")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			result, awaitErr := process.Await(ctx)
			if awaitErr != nil {
				t.Fatal(awaitErr)
			}
			if output := managedOutput(t, result); output.Outcome != planning.OutcomeAchieved {
				t.Fatalf("granted Action outcome = %s", output.Outcome)
			}
			if releaseErr := engine.ReleaseTree(ctx, process.Relation().ProcessID()); releaseErr != nil {
				t.Error(releaseErr)
			}
		})
	}
}

type planningActionBoundary struct {
	run.Store
	snapshot run.TreeSnapshot
	cause    error
}

func (p *planningActionBoundary) CommitEffect(ctx context.Context, boundary run.EffectBoundary) error {
	var envelope struct {
		Action json.RawMessage `json:"action"`
	}
	if boundary.Kind() == run.EffectBoundaryKindPending &&
		jsonv2.Unmarshal(boundary.Request().Effect().Payload(), &envelope, jsonv2.RejectUnknownMembers(false)) == nil && envelope.Action != nil {
		p.snapshot = boundary.TreeSnapshot()
		return p.cause
	}
	return p.Store.CommitEffect(ctx, boundary)
}

// This fixture represents a corrupted persistent image. It still fences each
// subsequent writer and head update; the restored binding must reject the
// damaged capability grant before an action executor can be reached.
type planningSnapshotCommitter struct {
	head  run.TreeSnapshot
	start run.StartReceipt
}

func (p *planningSnapshotCommitter) ActivateTree(_ context.Context, activation run.TreeActivation) error {
	if p.head.Digest() != activation.PreviousTreeDigest() || p.head.IncarnationID() != activation.PreviousIncarnationID() {
		return run.ErrTreeIncarnationConflict
	}
	p.head = activation.TreeSnapshot()
	return nil
}
func (p *planningSnapshotCommitter) advance(previous run.Digest, snapshot run.TreeSnapshot) error {
	if p.head.Digest() != previous || p.head.IncarnationID() != snapshot.IncarnationID() {
		return run.ErrTreeIncarnationConflict
	}
	p.head = snapshot
	return nil
}
func (p *planningSnapshotCommitter) CommitEffect(_ context.Context, boundary run.EffectBoundary) error {
	return p.advance(boundary.PreviousTreeDigest(), boundary.TreeSnapshot())
}
func (p *planningSnapshotCommitter) CommitCheckpoint(_ context.Context, checkpoint run.TreeCheckpoint) error {
	if err := p.advance(checkpoint.PreviousTreeDigest(), checkpoint.TreeSnapshot()); err != nil {
		return err
	}
	if r, ok := checkpoint.Start(); ok {
		p.start = r
	}
	return nil
}

func (p *planningSnapshotCommitter) LookupStart(_ context.Context, id run.StartID) (run.StartReceipt, bool, error) {
	return p.start, p.start.Valid() && p.start.ID == id, nil
}
