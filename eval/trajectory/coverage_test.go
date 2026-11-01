package trajectory_test

import (
	startrand "crypto/rand"

	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/eval/trajectory"
)

// The parent dispatches models and its children dispatch tools. Persistence
// checkpoints are Framework progress and add no external operation to coverage.
func interactionCoverage(events []run.Event) *trajectory.Coverage {
	coverage := &trajectory.Coverage{}
	seen := make(map[trajectory.EffectReference]bool)
	for _, event := range events {
		fact, started := event.EffectStarted()
		if !started || fact.Target() != run.EffectTargetDispatcher {
			continue
		}
		id, _ := event.EffectID()
		incarnation, _ := event.TreeIncarnationID()
		reference := trajectory.EffectReference{ProcessID: event.Relation().ProcessID(), TreeIncarnationID: incarnation, EffectID: id}
		if seen[reference] {
			continue
		}
		seen[reference] = true
		if event.Relation().IsRoot() {
			coverage.Models = append(coverage.Models, reference)
		} else {
			coverage.Tools = append(coverage.Tools, reference)
		}
	}
	return coverage
}

func TestCoverageClassifiesEffectsWithinOneDeployment(t *testing.T) {
	if err := (trajectory.Trajectory{}).CheckCoverage(); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatalf("invalid record coverage = %v", err)
	}
	recorded := coveredInteraction(t)
	if err := recorded.CheckCoverage(); err != nil {
		t.Fatal(err)
	}
	coverage := recorded.Coverage()
	if len(coverage.Models) != 2 || len(coverage.Tools) != 1 || len(coverage.Other) != 0 {
		t.Fatalf("coverage = %+v", coverage)
	}
	encoded, err := jsonv2.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	var restored trajectory.Trajectory
	if err := jsonv2.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(coverage, restored.Coverage()) {
		t.Fatal("coverage changed across the strict trajectory round trip")
	}
	for _, sample := range []struct {
		name   string
		change func(*trajectory.Coverage)
		want   error
	}{
		{"invalid identity", func(c *trajectory.Coverage) { c.Models[0].ProcessID = run.ProcessID{} }, trajectory.ErrInvalidTrajectory},
		{"duplicate classification", func(c *trajectory.Coverage) { c.Other = append(c.Other, c.Models[0]) }, trajectory.ErrInvalidTrajectory},
		{"missing classification", func(c *trajectory.Coverage) { c.Tools = nil }, trajectory.ErrIncompleteRecording},
		{"wrong incarnation", func(c *trajectory.Coverage) { c.Tools[0].TreeIncarnationID = run.TreeIncarnationID{} }, trajectory.ErrIncompleteRecording},
		{"tool declared as model", func(c *trajectory.Coverage) { c.Models = append(c.Models, c.Tools...); c.Tools = nil }, trajectory.ErrIncompleteRecording},
		{"extra classification", func(c *trajectory.Coverage) {
			unexpected := c.Tools[0]
			unexpected.EffectID, _ = run.ParseEffectID("effect:unobserved")
			c.Other = append(c.Other, unexpected)
		}, trajectory.ErrIncompleteRecording},
		{"unobserved effect", func(c *trajectory.Coverage) { c.Models[0].EffectID, _ = run.ParseEffectID("effect:unobserved") }, trajectory.ErrIncompleteRecording},
	} {
		t.Run(sample.name, func(t *testing.T) {
			config := trajectoryConfig(recorded)
			sample.change(config.Coverage)
			candidate, err := trajectory.New(config)
			if err == nil {
				err = candidate.CheckCoverage()
			}
			if !errors.Is(err, sample.want) {
				t.Fatalf("classification error = %v, want %v", err, sample.want)
			}
		})
	}
}

func TestCoveragePreservesExactlyOneSemanticObservationPerEffect(t *testing.T) {
	recorded := coveredInteraction(t)
	for _, sample := range []struct {
		name   string
		change func(*trajectory.Config)
	}{
		{"duplicate model response", func(c *trajectory.Config) {
			duplicate := c.ModelCalls[0].Clone()
			duplicate.CallSequence += 100
			c.ModelCalls = append(c.ModelCalls, duplicate)
		}},
		{"duplicate tool call", func(c *trajectory.Config) {
			c.ToolCalls = append(c.ToolCalls, c.ToolCalls[0].Clone())
		}},
		{"model classified as other", func(c *trajectory.Config) {
			c.Coverage.Other = append(c.Coverage.Other, c.Coverage.Models...)
			c.Coverage.Models = nil
		}},
		{"tool classified as other", func(c *trajectory.Config) {
			c.Coverage.Other = append(c.Coverage.Other, c.Coverage.Tools...)
			c.Coverage.Tools = nil
		}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			config := trajectoryConfig(recorded)
			sample.change(&config)
			candidate, err := trajectory.New(config)
			want := trajectory.ErrInvalidTrajectory
			if err == nil {
				err = candidate.CheckCoverage()
				want = trajectory.ErrIncompleteRecording
			}
			if !errors.Is(err, want) {
				t.Fatalf("semantic coverage = %v, want %v", err, want)
			}
		})
	}
}

func TestRecorderCoverageClassifiesSameIdentityReplaysOnce(t *testing.T) {
	for _, unknownAttempts := range []uint32{1, 2} {
		t.Run(fmt.Sprintf("unknown_attempts_%d", unknownAttempts), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				inputSchema, err := jsonschema.For[fixtureInput]()
				if err != nil {
					t.Fatal(err)
				}
				outputSchema, err := jsonschema.For[fixtureOutput]()
				if err != nil {
					t.Fatal(err)
				}
				descriptor, err := run.NewDescriptor(run.DescriptorConfig{
					Name: "test.coverage_replay", Description: "Classify one logical Effect across explicit replays.",
					InputSchema: inputSchema, OutputSchema: outputSchema,
				})
				if err != nil {
					t.Fatal(err)
				}
				requests := make(chan run.EffectRequest, unknownAttempts+1)
				dispatcher := &coverageReplayDispatcher{unknownAttempts: unknownAttempts, requests: requests}
				deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
					Definition: coverageReplayDefinition{descriptor: descriptor}, Dispatcher: dispatcher,
					ImplementationDigest: run.ComputeDigest([]byte("coverage-replay-implementation")),
					ConfigurationDigest:  run.ComputeDigest([]byte("coverage-replay-configuration")),
				})
				if err != nil {
					t.Fatal(err)
				}
				recorder := &trajectory.Recorder{}
				engine, err := run.New(t.Context(), run.Config{
					Store: run.NewMemoryStore(), EventListeners: []run.EventListener{recorder},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
						t.Error(closeErr)
					}
				}()
				input, err := run.EncodePayload(fixtureInput{Value: "original"})
				if err != nil {
					t.Fatal(err)
				}
				process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					ctx := context.WithoutCancel(t.Context())
					if killErr := process.Kill(ctx, "release replay recording"); killErr != nil && !errors.Is(killErr, run.ErrProcessFinished) {
						t.Error(killErr)
					}
					if joinErr := process.Join(ctx); joinErr != nil {
						t.Error(joinErr)
					}
				}()
				synctest.Wait()
				original := <-requests
				incarnation, bound := original.TreeIncarnationID()
				if !bound {
					t.Fatal("Engine request has no active writer incarnation")
				}
				reference := trajectory.EffectReference{
					ProcessID: process.Relation().ProcessID(), TreeIncarnationID: incarnation, EffectID: original.ID(),
				}
				for attempt := uint32(1); attempt <= unknownAttempts; attempt++ {
					replayErr := process.ReplayUnknownEffect(t.Context(), original.ID())
					if attempt < unknownAttempts {
						if !errors.Is(replayErr, run.ErrEffectOutcomeUnknown) {
							t.Fatalf("uncertain replay = %v", replayErr)
						}
					} else if replayErr != nil {
						t.Fatal(replayErr)
					}
					replayed := <-requests
					if replayed.ID() != original.ID() || replayed.Relation() != original.Relation() ||
						replayed.StepSequence() != original.StepSequence() || !bytes.Equal(replayed.Effect().Payload(), original.Effect().Payload()) {
						t.Fatal("replay changed the logical Effect request")
					}
				}
				recorded, err := recorder.Take(t.Context(), process, &trajectory.Coverage{Other: []trajectory.EffectReference{reference}})
				if err != nil {
					t.Fatalf("one classification for the replayed Effect: %v", err)
				}
				if recorded.Termination().Status() != run.StatusCompleted || recorded.RootUsage().PreparedEffects != 1 {
					t.Fatalf("replay outcome = %s, usage = %+v", recorded.Termination().Status(), recorded.RootUsage())
				}
				attempts := make(map[run.EffectAttemptID]bool)
				for _, event := range recorded.Events() {
					fact, started := event.EffectStarted()
					if !started || fact.Target() != run.EffectTargetDispatcher {
						continue
					}
					id, _ := event.EffectID()
					writer, _ := event.TreeIncarnationID()
					if id != reference.EffectID || event.Relation().ProcessID() != reference.ProcessID || writer != reference.TreeIncarnationID || attempts[fact.AttemptID()] {
						t.Fatal("attempts must retain the logical reference and have distinct attempt identities")
					}
					attempts[fact.AttemptID()] = true
				}
				if len(attempts) != int(unknownAttempts)+1 {
					t.Fatalf("recorded %d attempts, want %d", len(attempts), unknownAttempts+1)
				}
				encoded, err := jsonv2.Marshal(recorded)
				if err != nil {
					t.Fatal(err)
				}
				var restored trajectory.Trajectory
				if decodeErr := jsonv2.Unmarshal(encoded, &restored); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				reencoded, err := jsonv2.Marshal(restored)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(encoded, reencoded) {
					t.Fatal("round trip changed coverage or attempt facts")
				}
				config := trajectoryConfig(recorded)
				config.Coverage = &trajectory.Coverage{}
				empty, err := trajectory.New(config)
				if err != nil {
					t.Fatal(err)
				}
				if _, coverageErr := empty.TotalTokens(); !errors.Is(coverageErr, trajectory.ErrIncompleteRecording) {
					t.Fatalf("empty coverage for replayed Effect = %v", coverageErr)
				}
				config.Coverage = nil
				unknown, err := trajectory.New(config)
				if err != nil {
					t.Fatal(err)
				}
				if _, coverageErr := unknown.TotalTokens(); !errors.Is(coverageErr, trajectory.ErrIncompleteRecording) {
					t.Fatalf("undeclared replay coverage = %v", coverageErr)
				}
			})
		})
	}
}

type coverageReplayDefinition struct{ descriptor run.Descriptor }

func (c coverageReplayDefinition) Descriptor() run.Descriptor { return c.descriptor }

func (coverageReplayDefinition) ChildDeployments() []run.Program[run.Payload, run.Payload] {
	return nil
}

func (coverageReplayDefinition) Start(input run.Payload) (run.Execution, error) {
	value, err := input.Decode[fixtureInput]()
	if err != nil {
		return nil, err
	}
	return &coverageReplayExecution{Value: value.Value}, nil
}

func (coverageReplayDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var execution coverageReplayExecution
	if err := jsonv2.Unmarshal(state.Payload(), &execution); err != nil {
		return nil, err
	}
	return &execution, nil
}

type coverageReplayExecution struct {
	Value  string `json:"value"`
	Issued bool   `json:"issued"`
}

func (c *coverageReplayExecution) Step(_ context.Context, signals []run.Signal) (run.Transition, error) {
	if !c.Issued {
		payload, err := run.EncodePayload(fixtureInput{Value: c.Value})
		if err != nil {
			return run.Transition{}, err
		}
		effect, err := run.NewDispatcherEffect(payload.JSON())
		if err != nil {
			return run.Transition{}, err
		}
		c.Issued = true
		return run.Continue(0, effect)
	}
	if len(signals) != 1 || !signals[0].EngineOwned() {
		return run.Transition{}, run.ErrInvalidExecutionState
	}
	output, err := run.ParsePayload(signals[0].Payload())
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(1, output)
}

func (c *coverageReplayExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState("test.coverage_replay", c)
}

type coverageReplayDispatcher struct {
	unknownAttempts uint32
	calls           atomic.Uint32
	requests        chan<- run.EffectRequest
}

func (c *coverageReplayDispatcher) Dispatch(_ context.Context, request run.EffectRequest, _ run.DeltaEmitter) (run.Settlement, error) {
	c.requests <- request
	if c.calls.Add(1) <= c.unknownAttempts {
		return run.Settlement{}, errors.New("fixture outcome is uncertain")
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, []byte(`{"value":"confirmed"}`))
}

func (*coverageReplayDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicySameIdentity}
}
