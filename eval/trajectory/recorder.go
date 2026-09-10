package trajectory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

// ErrIncompleteRecording reports observations that cannot form a trustworthy Trajectory.
var ErrIncompleteRecording = errors.New("eval/trajectory: incomplete recording")

type modelObservation struct {
	root agent.ProcessID
	call ModelCall
}

type toolIdentity struct {
	processID    agent.ProcessID
	effectID     agent.EffectID
	stepSequence uint64
	modelCall    uint32
	index        uint32
}

type toolObservation struct {
	root    agent.ProcessID
	call    ToolCall
	started bool
	settled bool
	invalid bool
}

// Recorder joins the Agent mechanics and Interaction semantic observation
// boundaries into owned Trajectories. Take consumes one completed root tree so
// a long-lived Engine does not turn evaluation capture into an unbounded log.
type Recorder struct {
	mu     sync.Mutex
	events map[agent.ProcessID][]agent.Event
	models []modelObservation
	tools  map[toolIdentity]toolObservation
}

func (r *Recorder) OnEvent(_ context.Context, event agent.Event) {
	if r == nil || !event.Valid() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.events == nil {
		r.events = make(map[agent.ProcessID][]agent.Event)
	}
	root := event.Relation().RootID()
	r.events[root] = append(r.events[root], event)
}

func (r *Recorder) OnModelResponse(
	_ context.Context,
	invocation interaction.ModelInvocation,
	response *chat.Response,
) {
	if r == nil || !invocation.Valid() || response == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models = append(r.models, modelObservation{
		root: invocation.Relation().RootID(),
		call: ModelCall{
			ProcessID: invocation.Relation().ProcessID(), StepSequence: invocation.StepSequence(),
			CallSequence: invocation.ModelCallSequence(), Response: response.Clone(),
		},
	})
}

func (r *Recorder) OnToolStarted(_ context.Context, invocation interaction.ToolInvocation) {
	if r == nil || !invocation.Valid() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = make(map[toolIdentity]toolObservation)
	}
	identity := toolIdentityOf(invocation)
	observation := r.tools[identity]
	observation.invalid = observation.invalid || observation.started
	observation.root = invocation.Relation().RootID()
	observation.call = ToolCall{
		ProcessID: invocation.Relation().ProcessID(), StepSequence: invocation.StepSequence(),
		ModelCall: invocation.ModelCallSequence(), Index: invocation.ToolCallIndex(),
		Call: invocation.ToolCall(),
	}
	observation.started = true
	r.tools[identity] = observation
}

func (r *Recorder) OnToolSettled(
	_ context.Context,
	invocation interaction.ToolInvocation,
	settlement interaction.ToolSettlement,
) {
	if r == nil || !invocation.Valid() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = make(map[toolIdentity]toolObservation)
	}
	identity := toolIdentityOf(invocation)
	observation := r.tools[identity]
	observation.invalid = observation.invalid || observation.settled
	if !observation.started {
		observation.root = invocation.Relation().RootID()
		observation.call = ToolCall{
			ProcessID: invocation.Relation().ProcessID(), StepSequence: invocation.StepSequence(),
			ModelCall: invocation.ModelCallSequence(), Index: invocation.ToolCallIndex(),
			Call: invocation.ToolCall(),
		}
	}
	observation.call.Outcome, observation.call.Result, observation.call.Failure = recordedOutcome(settlement)
	observation.settled = true
	r.tools[identity] = observation
}

// Take returns and releases the complete recording for one terminal root
// Result. The result must belong to the root Process, not one child.
// Incomplete observations remain available if validation fails.
func (r *Recorder) Take(result agent.Result) (Trajectory, error) {
	if r == nil || !result.Valid() || !result.ProcessID().Valid() {
		return Trajectory{}, fmt.Errorf("%w: terminal root result is required", ErrIncompleteRecording)
	}
	root := result.ProcessID()
	r.mu.Lock()
	defer r.mu.Unlock()
	events := r.events[root]
	models := recordedModels(r.models, root)
	tools, recordingErr := recordedTools(r.tools, root)
	if recordingErr != nil {
		return Trajectory{}, recordingErr
	}
	var output *agent.Output
	if value, present := result.Output(); present {
		output = &value
	}
	recorded, err := New(Config{
		RootProcessID: root,
		Termination:   result.Termination(),
		Output:        output,
		Usage:         result.Usage(),
		Duration:      result.FinishedAt().Sub(result.StartedAt()),
		Events:        events,
		ModelCalls:    models,
		ToolCalls:     tools,
	})
	if err != nil {
		return Trajectory{}, err
	}
	delete(r.events, root)
	r.models = slices.DeleteFunc(r.models, func(observation modelObservation) bool {
		return observation.root == root
	})
	for identity, observation := range r.tools {
		if observation.root == root {
			delete(r.tools, identity)
		}
	}
	return recorded, nil
}

func toolIdentityOf(invocation interaction.ToolInvocation) toolIdentity {
	return toolIdentity{
		processID: invocation.Relation().ProcessID(), effectID: invocation.EffectID(),
		stepSequence: invocation.StepSequence(), modelCall: invocation.ModelCallSequence(),
		index: invocation.ToolCallIndex(),
	}
}

func recordedOutcome(
	settlement interaction.ToolSettlement,
) (ToolOutcome, *chat.ToolResult, string) {
	modes := 0
	if settlement.Result != nil {
		modes++
	}
	if settlement.InputRequired {
		modes++
	}
	if settlement.Failure != "" && !settlement.Unknown {
		modes++
	}
	if settlement.Unknown {
		modes++
	}
	if modes != 1 {
		return ToolOutcomeInvalid, nil, ""
	}
	if settlement.Result != nil {
		result := settlement.Result.Clone()
		if result.IsError {
			return ToolOutcomeError, &result, ""
		}
		return ToolOutcomeSucceeded, &result, ""
	}
	if settlement.InputRequired {
		return ToolOutcomeInputRequired, nil, ""
	}
	if settlement.Unknown {
		return ToolOutcomeUnknown, nil, settlement.Failure
	}
	if settlement.Failure != "" {
		return ToolOutcomeFailed, nil, settlement.Failure
	}
	return ToolOutcomeInvalid, nil, ""
}

func recordedModels(observations []modelObservation, root agent.ProcessID) []ModelCall {
	var calls []ModelCall
	for _, observation := range observations {
		if observation.root == root {
			calls = append(calls, observation.call.Clone())
		}
	}
	return calls
}

func recordedTools(
	observations map[toolIdentity]toolObservation,
	root agent.ProcessID,
) ([]ToolCall, error) {
	var calls []ToolCall
	var recordingErr error
	for _, observation := range observations {
		if observation.root != root {
			continue
		}
		if observation.invalid || !observation.started || !observation.settled || !observation.call.Outcome.Valid() {
			if recordingErr == nil {
				recordingErr = fmt.Errorf("%w: tool call %q did not publish exactly one valid start and settlement", ErrIncompleteRecording, observation.call.Call.Name)
			}
			continue
		}
		calls = append(calls, observation.call.Clone())
	}
	return calls, recordingErr
}

var (
	_ agent.EventListener       = (*Recorder)(nil)
	_ interaction.ModelObserver = (*Recorder)(nil)
	_ interaction.ToolObserver  = (*Recorder)(nil)
)
