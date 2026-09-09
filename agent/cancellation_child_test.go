package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"testing/synctest"
)

func TestCancellationReachesChildrenBeforeAncestorDispatchReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		childDispatcher := &cancellationDispatcher{
			entered: make(chan EffectRequest, 1), canceled: make(chan struct{}),
			release: make(chan struct{}), status: SettlementStatusSucceeded,
		}
		rootDispatcher := &cancellationDispatcher{
			entered: make(chan EffectRequest, 1), canceled: make(chan struct{}),
			release: make(chan struct{}), status: SettlementStatusSucceeded, frontier: 1,
		}
		releaseRoot := sync.OnceFunc(func() { close(rootDispatcher.release) })
		releaseChild := sync.OnceFunc(func() { close(childDispatcher.release) })
		defer releaseRoot()
		defer releaseChild()
		childDeployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), childDispatcher)
		input, _ := EncodeInput(engineTestInput{Value: "owned"})
		key, _ := ParseChildKey("worker")
		childEffect, err := StartChild(childTestSpec(key, childDeployment.DeploymentRef(), input))
		if err != nil {
			t.Fatal(err)
		}
		external, _ := NewDispatcherEffect(json.RawMessage(`{}`))
		definition := &effectSequenceDefinition{
			descriptor: newEngineTestDefinition(t, "engine.owner", "effect").Descriptor(),
			effects:    []Effect{childEffect, external},
		}
		engine, err := NewEngine(EngineConfig{DeploymentResolver: deploymentResolverFunc(func(DeploymentRef) (Deployment, error) {
			return childDeployment, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		root, err := engine.Start(t.Context(), engineTestDeployment(t, definition, rootDispatcher), input)
		if err != nil {
			t.Fatal(err)
		}
		<-rootDispatcher.entered
		<-childDispatcher.entered
		if err := root.Kill(t.Context(), "stop the scope"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		for name, dispatcher := range map[string]*cancellationDispatcher{"root": rootDispatcher, "child": childDispatcher} {
			select {
			case <-dispatcher.canceled:
			default:
				t.Errorf("%s Dispatch did not receive cancellation", name)
			}
		}
		releaseChild()
		childIDs := directChildIDs(t, engine, root.ID())
		if len(childIDs) != 1 {
			t.Fatalf("children = %v", childIDs)
		}
		childID, _ := ParseProcessID(childIDs[0])
		child, _ := engine.Process(childID)
		if result := mustAwait(t, child); result.Status() != StatusCanceled || result.Termination().Cause() != TerminationCauseParentCancellation {
			t.Errorf("child termination = %+v", result.Termination())
		}
		if inspectProcessSnapshot(t, root).Status().Terminal() {
			t.Error("ancestor became terminal before its Dispatch returned")
		}
		releaseRoot()
		_ = mustAwait(t, root)
		mustCloseEngine(t, engine)
	})
}

func TestCancellationCollectsInFlightChildInitialization(t *testing.T) {
	for _, stage := range []string{"rejected admission", "accepted admission", "outcome acknowledgment"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered := make(chan context.Context, 1)
				release := make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				defer unblock()
				var outcomes []ProcessStartOutcome
				config := EngineConfig{
					TreeDurability: &recordingTreeDurability{},
					ProcessAdmitter: ProcessAdmitterFunc(func(ctx context.Context, admission ProcessAdmission) error {
						if admission.Relation().IsRoot() || stage == "outcome acknowledgment" {
							return nil
						}
						entered <- ctx
						<-release
						if stage == "rejected admission" {
							return ctx.Err()
						}
						return nil
					}),
					ProcessStartOutcomeAcknowledger: ProcessStartOutcomeAcknowledgerFunc(func(ctx context.Context, outcome ProcessStartOutcome) error {
						if outcome.Admission().Relation().IsRoot() {
							return nil
						}
						if stage == "outcome acknowledgment" {
							entered <- ctx
							<-release
						}
						if ctx.Err() != nil || ctx.Done() != nil {
							t.Error("cancellation abandoned required initialization acknowledgment")
						}
						outcomes = append(outcomes, outcome)
						return nil
					}),
				}
				engine, err := NewEngine(config)
				if err != nil {
					t.Fatal(err)
				}
				deployment := newChildTestDeployment(t)
				input, _ := EncodeInput(childTestInput{Mode: "parent"})
				root, err := engine.Start(t.Context(), deployment, input)
				if err != nil {
					t.Fatal(err)
				}
				jobContext := <-entered
				if killErr := root.Kill(t.Context(), "stop during initialization"); killErr != nil {
					t.Fatal(killErr)
				}
				synctest.Wait()
				if stage != "outcome acknowledgment" && jobContext.Err() != context.Canceled {
					t.Error("child admission did not receive cancellation")
				}
				if inspectProcessSnapshot(t, root).Status().Terminal() {
					t.Error("parent became terminal before child initialization returned")
				}
				signalID, _ := ParseSignalID("signal:child-start-cancellation-cut")
				signal, _ := NewSignalRequest(signalID, WaitID{}, json.RawMessage(`{}`))
				if accepted, deliveryErr := root.DeliverSignals(t.Context(), signal); deliveryErr != nil || !accepted {
					t.Fatalf("input admission = %t, %v", accepted, deliveryErr)
				}
				checkpoints := config.TreeDurability.(*recordingTreeDurability).treeCheckpoints()
				interrupted := checkpoints[len(checkpoints)-1].TreeSnapshot()
				unblock()
				if result := mustAwait(t, root); result.Status() != StatusKilled {
					t.Fatalf("parent termination = %+v", result.Termination())
				}
				children := directChildIDs(t, engine, root.ID())
				wire, err := inspectProcessSnapshot(t, root).wire()
				if err != nil {
					t.Fatal(err)
				}
				if stage == "rejected admission" {
					if len(children) != 0 || len(outcomes) != 0 || wire.ReservedBudget != (Budget{}) || wire.Prepared.Effects[0].Settlement.Status() != SettlementStatusFailed {
						t.Errorf("rejected admission published resources: children=%v outcomes=%v snapshot=%+v", children, outcomes, wire)
					}
				} else {
					if len(children) != 1 || len(outcomes) != 1 || wire.ReservedBudget == (Budget{}) || wire.Prepared.Effects[0].Settlement.Status() != SettlementStatusSucceeded {
						t.Fatalf("accepted initialization was lost: children=%v outcomes=%v snapshot=%+v", children, outcomes, wire)
					}
					childID, _ := ParseProcessID(children[0])
					child, _ := engine.Process(childID)
					result := mustAwait(t, child)
					if result.Status() != StatusCanceled || result.Usage().CommittedSteps != 0 {
						t.Errorf("late child ran after cancellation: %+v", result)
					}
				}
				mustCloseEngine(t, engine)
				priorOutcomes := len(outcomes)
				config.TreeDurability = &recordingTreeDurability{}
				recoveredEngine, err := NewEngine(config)
				if err != nil {
					t.Fatal(err)
				}
				recovered, err := recoveredEngine.RestoreTree(t.Context(), deployment, interrupted)
				if err != nil {
					t.Fatal(err)
				}
				if result := mustAwait(t, recovered); result.Status() != StatusKilled || len(result.Termination().UnresolvedEffectIDs()) != 0 {
					t.Fatalf("restored child publication termination = %+v", result.Termination())
				}
				recoveredWire, err := inspectProcessSnapshot(t, recovered).wire()
				if err != nil {
					t.Fatal(err)
				}
				settlement := recoveredWire.Prepared.Effects[0].Settlement
				if settlement == nil || settlement.Status() != SettlementStatusFailed {
					t.Fatalf("interrupted child publication = %+v", settlement)
				}
				start, err := decodeChildStartResult(settlement.Payload())
				failure, failed := start.Failure()
				if err != nil || !failed || failure.Code() != childStartInterruptedCode ||
					recoveredWire.ReservedBudget != (Budget{}) || len(directChildIDs(t, recoveredEngine, recovered.ID())) != 0 ||
					len(outcomes) != priorOutcomes {
					t.Errorf("recovery repeated or lost unpublished initialization: result=%+v error=%v budget=%+v outcomes=%d", start, err, recoveredWire.ReservedBudget, len(outcomes))
				}
				mustCloseEngine(t, recoveredEngine)
			})
		})
	}
}
