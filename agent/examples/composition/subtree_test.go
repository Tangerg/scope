package main

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

func TestCompositionRejectsCompletedChildWithUnresolvedDescendant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		child, _ := newUncertainModelDeployment(t)
		model := completedSubtreeDeployment(child)
		fixture := newCompositionFixture(t, model)
		observations := &agenttest.ObservationRecorder{}
		engine := newCompositionEngine(t, agent.EngineConfig{
			TreeCommitter: agent.NewMemoryTreeCommitter(), EventListeners: []agent.EventListener{observations},
		})
		input := compositionValue(agent.EncodePayload(compositionInput{Prompt: "uncertain work"}))
		root := compositionValue(engine.Start(t.Context(), fixture.composition, input))
		unknown := compositionValue(observations.AwaitEvent(t.Context(), func(event agent.Event) bool {
			finished, present := event.EffectFinished()
			return present && finished.SettlementStatus() == agent.SettlementStatusUnknown
		}))
		synctest.Wait()
		inspection := compositionValue(engine.InspectTree(t.Context(), root.Relation().ProcessID()))
		var completedChild *agent.Process
		for _, process := range inspection.Processes {
			if process.Snapshot.DeploymentRef() == model.DeploymentRef() {
				if process.Snapshot.Status() != agent.StatusPaused {
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
		if result.Termination().Status() != agent.StatusFailed || !failed ||
			failure.Kind() != agent.FailureKindExternal || failure.Code() != "workflow.fork.branch_unresolved_effects" {
			t.Fatalf("unresolved subtree was adopted: %+v", result.Termination())
		}
		if _, present := result.Termination().Output(); present {
			t.Fatal("unresolved work escaped as ordinary composition output")
		}
		if result := compositionValue(completedChild.Await(t.Context())); result.Termination().Status() != agent.StatusCompleted {
			t.Fatalf("fixture did not publish a completed child: %+v", result.Termination())
		}
		descendant, present := engine.Process(unknown.Relation().ProcessID())
		if !present {
			t.Fatal("unknown descendant was lost")
		}
		descendantResult := compositionValue(descendant.Await(t.Context()))
		effectID, _ := unknown.EffectID()
		unresolved := descendantResult.Termination().UnresolvedEffectIDs()
		if descendantResult.Termination().Status() != agent.StatusCanceled || len(unresolved) != 1 || unresolved[0] != effectID {
			t.Fatalf("descendant uncertainty was lost: %+v", descendantResult.Termination())
		}
	})
}

const completedSubtreeStateKind = "test.composition.completed_subtree"

// This fixture deliberately publishes its own result before an uncertain
// descendant resolves. The composition must apply Workflow's adoption policy.
type completedSubtreeDefinition struct {
	descriptor agent.Descriptor
	child      agent.Deployment
	output     agent.Payload
}

func completedSubtreeDeployment(child agent.Deployment) agent.Deployment {
	descriptor := compositionValue(agent.NewDescriptor(agent.DescriptorConfig{
		Name: "test.completed_subtree", Description: "Complete while a descendant retains an unknown Effect.",
		InputSchema: child.Descriptor().InputSchema(), OutputSchema: child.Descriptor().OutputSchema(),
	}))
	message := chat.NewAssistantMessage(chat.NewTextPart("a result with unresolved work"))
	response := compositionValue(chat.NewResponse(&chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}, nil))
	return compositionValue(agent.NewDeployment(agent.DeploymentConfig{
		Definition: &completedSubtreeDefinition{
			descriptor: descriptor, child: child,
			output: compositionValue(agent.EncodePayload(interaction.Output{ModelResponse: response})),
		},
		ImplementationDigest: agent.ComputeDigest([]byte("completed-subtree-fixture")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("pause-before-completion")),
	}))
}

func (c *completedSubtreeDefinition) Descriptor() agent.Descriptor { return c.descriptor }

func (c *completedSubtreeDefinition) ChildDeployments() []agent.Deployment {
	return []agent.Deployment{c.child}
}

func (c *completedSubtreeDefinition) Start(input agent.Payload) (agent.Execution, error) {
	if err := c.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	return &completedSubtreeExecution{definition: c, state: completedSubtreeState{Input: input}}, nil
}

func (c *completedSubtreeDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	decoded, err := state.Decode[completedSubtreeState](completedSubtreeStateKind)
	if err != nil {
		return nil, err
	}
	if decoded.Phase > completedSubtreeFinished {
		return nil, agent.ErrInvalidExecutionState
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
	Input agent.Payload         `json:"input"`
}

type completedSubtreeExecution struct {
	definition *completedSubtreeDefinition
	state      completedSubtreeState
}

func (c *completedSubtreeExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	switch c.state.Phase {
	case completedSubtreeReady:
		key, err := agent.ParseChildKey("uncertain")
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.NewChildStartEffect(agent.ChildSpec{
			Key: key, DeploymentRef: c.definition.child.DeploymentRef(), Input: c.state.Input,
			Budget: agent.Budget{Steps: agent.NewQuota(4), Effects: agent.NewQuota(2), Signals: agent.NewQuota(4)},
		})
		if err != nil {
			return agent.Transition{}, err
		}
		c.state.Phase = completedSubtreeStarting
		return agent.Continue(0, effect)
	case completedSubtreeStarting:
		if len(signals) != 1 {
			return agent.Transition{}, agent.ErrInvalidSignal
		}
		started, err := agent.ParseChildStartResult(signals[0])
		if err != nil {
			return agent.Transition{}, err
		}
		if _, present := started.ProcessID(); !present {
			return agent.Transition{}, errors.New("uncertain descendant was not admitted")
		}
		c.state.Phase = completedSubtreeFinishing
		return agent.Pause(1, "wait for the descendant's uncertain outcome")
	case completedSubtreeFinishing:
		c.state.Phase = completedSubtreeFinished
		return agent.Complete(0, c.definition.output)
	default:
		return agent.Transition{}, agent.ErrInvalidExecutionState
	}
}

func (c *completedSubtreeExecution) Snapshot() (agent.ExecutionState, error) {
	return agent.EncodeExecutionState(completedSubtreeStateKind, c.state)
}
