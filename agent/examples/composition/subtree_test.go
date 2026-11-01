package main

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
	"github.com/Tangerg/scope/core/chat"
)

func TestCompositionRejectsCompletedChildWithUnresolvedDescendant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		child, _ := newUncertainModelDeployment(t)
		model := completedSubtreeDeployment(child.Erase())
		fixture := newCompositionFixture(t, model)
		observations := &runtest.ObservationRecorder{}
		engine := newCompositionEngine(t, agentrun.Config{
			Store: agentrun.NewMemoryStore(), EventListeners: []agentrun.EventListener{observations},
		})
		input := compositionValue(agentrun.EncodePayload(compositionInput{Prompt: "uncertain work"}))
		root := compositionValue(engine.Start(t.Context(), fixture.composition, agentrun.StartRequest[agentrun.Payload]{ID: agentrun.StartID{Scope: "test", Key: startrand.Text()}, Input: input}))
		unknown := compositionValue(observations.AwaitEvent(t.Context(), func(event agentrun.Event) bool {
			finished, present := event.EffectFinished()
			return present && finished.SettlementStatus() == agentrun.SettlementStatusUnknown
		}))
		synctest.Wait()
		inspection := compositionValue(engine.InspectTree(t.Context(), root.Relation().ProcessID()))
		var completedChild *agentrun.Handle[agentrun.Payload]
		for _, process := range inspection.Processes {
			if process.Snapshot.ProgramRef() == model.ProgramRef() {
				if process.Snapshot.Status() != agentrun.StatusPaused {
					t.Fatalf("fixture status = %s, want paused", process.Snapshot.Status())
				}
				completedChild, _ = engine.Process(process.Snapshot.Relation().ProcessID())
			}
		}
		if completedChild == nil {
			t.Fatal("completed-subtree fixture is missing")
		}
		if err := completedChild.Resume(t.Context()); err != nil {
			t.Fatal(err)
		}
		result := compositionValue(root.Await(t.Context()))
		if err := root.Join(t.Context()); err != nil {
			t.Fatal(err)
		}
		failure, failed := result.Termination().Failure()
		if result.Termination().Status() != agentrun.StatusFailed || !failed ||
			failure.Kind() != agentrun.FailureKindExternal || failure.Code() != "workflow.fork.branch_unresolved_effects" {
			t.Fatalf("unresolved subtree was adopted: %+v", result.Termination())
		}
		if _, present := result.Termination().Output(); present {
			t.Fatal("unresolved work escaped as ordinary composition output")
		}
		if result := compositionValue(completedChild.Await(t.Context())); result.Termination().Status() != agentrun.StatusCompleted {
			t.Fatalf("fixture did not publish a completed child: %+v", result.Termination())
		}
		descendant, present := engine.Process(unknown.Relation().ProcessID())
		if !present {
			t.Fatal("unknown descendant was lost")
		}
		descendantResult := compositionValue(descendant.Await(t.Context()))
		effectID, _ := unknown.EffectID()
		unresolved := descendantResult.Termination().UnresolvedEffectIDs()
		if descendantResult.Termination().Status() != agentrun.StatusCanceled || len(unresolved) != 1 || unresolved[0] != effectID {
			t.Fatalf("descendant uncertainty was lost: %+v", descendantResult.Termination())
		}
	})
}

const completedSubtreeStateKind = "test.composition.completed_subtree"

// This fixture deliberately publishes its own result before an uncertain
// descendant resolves. The composition must apply Workflow's adoption policy.
type completedSubtreeDefinition struct {
	descriptor agentrun.Descriptor
	child      agentrun.Program[agentrun.Payload, agentrun.Payload]
	output     agentrun.Payload
}

func completedSubtreeDeployment(child agentrun.Program[agentrun.Payload, agentrun.Payload]) agentrun.Program[agent.Input, agent.Output] {
	descriptor := compositionValue(agentrun.NewDescriptor(agentrun.DescriptorConfig{
		Name: "test.completed_subtree", Description: "Complete while a descendant retains an unknown Effect.",
		InputSchema: child.Descriptor().InputSchema(), OutputSchema: child.Descriptor().OutputSchema(),
	}))
	message := chat.NewAssistantMessage(chat.NewTextPart("a result with unresolved work"))
	response := compositionValue(chat.NewResponse(&chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}, nil))
	return compositionValue(agentrun.Bind[agent.Input, agent.Output](agentrun.BindingConfig{
		Definition: &completedSubtreeDefinition{
			descriptor: descriptor, child: child,
			output: compositionValue(agentrun.EncodePayload(agent.Output{ModelResponse: response})),
		},
		ImplementationDigest: agentrun.ComputeDigest([]byte("completed-subtree-fixture")),
		ConfigurationDigest:  agentrun.ComputeDigest([]byte("pause-before-completion")),
	}))
}

func (c *completedSubtreeDefinition) Descriptor() agentrun.Descriptor { return c.descriptor }

func (c *completedSubtreeDefinition) ChildDeployments() []agentrun.Program[agentrun.Payload, agentrun.Payload] {
	return []agentrun.Program[agentrun.Payload, agentrun.Payload]{c.child}
}

func (c *completedSubtreeDefinition) Start(input agentrun.Payload) (agentrun.Execution, error) {
	if err := c.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	return &completedSubtreeExecution{definition: c, state: completedSubtreeState{Input: input}}, nil
}

func (c *completedSubtreeDefinition) Restore(ctx context.Context, state agentrun.ExecutionState) (agentrun.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	decoded, err := state.Decode[completedSubtreeState](completedSubtreeStateKind)
	if err != nil {
		return nil, err
	}
	if decoded.Phase > completedSubtreeFinished {
		return nil, agentrun.ErrInvalidExecutionState
	}
	if err := c.descriptor.ValidateInput(decoded.Input); err != nil {
		return nil, err
	}
	return &completedSubtreeExecution{definition: c, state: decoded}, nil
}

type completedSubtreePhase uint8

const (
	completedSubtreeReady completedSubtreePhase = iota
	completedSubtreeStarting
	completedSubtreeFinishing
	completedSubtreeFinished
)

type completedSubtreeState struct {
	Phase completedSubtreePhase `json:"phase"`
	Input agentrun.Payload      `json:"input"`
}

type completedSubtreeExecution struct {
	definition *completedSubtreeDefinition
	state      completedSubtreeState
}

func (c *completedSubtreeExecution) Step(ctx context.Context, signals []agentrun.Signal) (agentrun.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agentrun.Transition{}, err
	}
	switch c.state.Phase {
	case completedSubtreeReady:
		key, err := agentrun.ParseChildKey("uncertain")
		if err != nil {
			return agentrun.Transition{}, err
		}
		effect, err := agentrun.NewChildStartEffect(agentrun.ChildSpec{
			Key: key, ProgramRef: c.definition.child.ProgramRef(), Input: c.state.Input,
			Budget: agentrun.Budget{Steps: agentrun.NewQuota(4), Effects: agentrun.NewQuota(2), Signals: agentrun.NewQuota(4)},
		})
		if err != nil {
			return agentrun.Transition{}, err
		}
		c.state.Phase = completedSubtreeStarting
		return agentrun.Continue(0, effect)
	case completedSubtreeStarting:
		if len(signals) != 1 {
			return agentrun.Transition{}, agentrun.ErrInvalidSignal
		}
		started, err := agentrun.ParseChildStartResult(signals[0])
		if err != nil {
			return agentrun.Transition{}, err
		}
		if _, present := started.ProcessID(); !present {
			return agentrun.Transition{}, errors.New("uncertain descendant was not admitted")
		}
		c.state.Phase = completedSubtreeFinishing
		return agentrun.Pause(1, "wait for the descendant's uncertain outcome")
	case completedSubtreeFinishing:
		c.state.Phase = completedSubtreeFinished
		return agentrun.Complete(0, c.definition.output)
	default:
		return agentrun.Transition{}, agentrun.ErrInvalidExecutionState
	}
}

func (c *completedSubtreeExecution) Snapshot() (agentrun.ExecutionState, error) {
	return agentrun.EncodeExecutionState(completedSubtreeStateKind, c.state)
}
