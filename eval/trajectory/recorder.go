package trajectory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

var ErrIncompleteRecording = errors.New("eval/trajectory: incomplete recording")

// Recording limits bound retained evidence. Overflow invalidates that recording
// instead of silently converting missing observations into successful evidence.
const (
	MaxRecordingTrees  = 64
	MaxRecordingEvents = 65536
	MaxRecordingCalls  = 8192
)

type toolIdentity struct {
	processID   agent.ProcessID
	incarnation agent.TreeIncarnationID
	effectID    agent.EffectID
}
type toolObservation struct {
	call    ToolCall
	started bool
	settled bool
	invalid bool
}

type recording struct {
	// Callbacks that already found this session must not mutate exported evidence.
	sealed    bool
	mu        sync.Mutex
	events    []agent.Event
	models    []ModelCall
	tools     map[toolIdentity]toolObservation
	startedAt time.Time
	err       error
}

// Recorder's zero value accepts bounded concurrent tree recordings. Only root
// start/restore events open a session. Take joins the subtree, detaches the
// session, and validates outside shared locks. Take consumes failed recordings
// too; Discard releases trees whose RuntimeError produced no Result.
type Recorder struct {
	mu    sync.Mutex
	trees map[agent.ProcessID]*recording
}

func (r *Recorder) session(root agent.ProcessID) *recording {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trees[root]
}

func (r *Recorder) OnEvent(_ context.Context, event agent.Event) {
	if r == nil || !event.Valid() {
		return
	}
	root := event.Relation().RootID()
	r.mu.Lock()
	if r.trees == nil {
		r.trees = make(map[agent.ProcessID]*recording)
	}
	entry := r.trees[root]
	opensSession := event.Relation().IsRoot() &&
		(event.Name() == agent.EventProcessStarted || event.Name() == agent.EventProcessRestored)
	if entry == nil && opensSession && len(r.trees) < MaxRecordingTrees {
		entry = &recording{tools: make(map[toolIdentity]toolObservation)}
		if event.Name() == agent.EventProcessStarted {
			entry.startedAt = time.Now()
		}
		r.trees[root] = entry
	}
	r.mu.Unlock()
	if entry == nil {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.sealed {
		return
	}
	if len(entry.events) >= MaxRecordingEvents {
		entry.err = fmt.Errorf("%w: event capacity exceeded", ErrIncompleteRecording)
		return
	}
	if event.Name() == agent.EventProcessRestored {
		entry.startedAt = time.Time{}
	}
	entry.events = append(entry.events, event)
}

func (r *Recorder) OnModelResponse(_ context.Context, invocation interaction.ModelInvocation, response *chat.Response) {
	entry := r.session(invocation.Relation().RootID())
	if entry == nil || !invocation.Valid() || response == nil {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.sealed {
		return
	}
	if len(entry.models)+len(entry.tools) >= MaxRecordingCalls {
		entry.err = fmt.Errorf("%w: call capacity exceeded", ErrIncompleteRecording)
		return
	}
	incarnation, _ := invocation.TreeIncarnationID()
	entry.models = append(entry.models, ModelCall{
		ProcessID: invocation.Relation().ProcessID(), TreeIncarnationID: incarnation,
		EffectID: invocation.EffectID(), StepSequence: invocation.StepSequence(),
		CallSequence: invocation.ModelCallSequence(), Response: response,
	})
}

func (r *Recorder) OnToolStarted(_ context.Context, invocation interaction.ToolInvocation) {
	entry := r.session(invocation.Relation().RootID())
	if entry == nil || !invocation.Valid() {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.sealed {
		return
	}
	if len(entry.models)+len(entry.tools) >= MaxRecordingCalls {
		entry.err = fmt.Errorf("%w: call capacity exceeded", ErrIncompleteRecording)
		return
	}
	identity, call := recordedInvocation(invocation)
	observation := entry.tools[identity]
	observation.invalid = observation.invalid || observation.started
	observation.call = call
	observation.started = true
	entry.tools[identity] = observation
}

func (r *Recorder) OnToolSettled(_ context.Context, invocation interaction.ToolInvocation, settlement interaction.ToolSettlement) {
	entry := r.session(invocation.Relation().RootID())
	if entry == nil || !invocation.Valid() {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.sealed {
		return
	}
	identity, call := recordedInvocation(invocation)
	observation, exists := entry.tools[identity]
	if !exists && len(entry.models)+len(entry.tools) >= MaxRecordingCalls {
		entry.err = fmt.Errorf("%w: call capacity exceeded", ErrIncompleteRecording)
		return
	}
	observation.invalid = observation.invalid || observation.settled
	if !observation.started {
		observation.call = call
	}
	observation.call.Outcome, observation.call.Result, observation.call.Failure = recordedOutcome(settlement)
	observation.settled = true
	entry.tools[identity] = observation
}

// Take waits for root and descendant work to settle before consuming evidence.
// Coverage is an exhaustive Host declaration; nil exports observations without
// asserting semantic completeness. A caller timeout preserves the session.
func (r *Recorder) Take(ctx context.Context, process *agent.Process, coverage *Coverage) (Trajectory, error) {
	if r == nil || process == nil {
		return Trajectory{}, fmt.Errorf("%w: root process is required", ErrIncompleteRecording)
	}
	if err := process.Join(ctx); err != nil {
		return Trajectory{}, err
	}
	result, err := process.Await(ctx)
	if err != nil {
		return Trajectory{}, err
	}
	root := result.ProcessID()
	r.mu.Lock()
	entry := r.trees[root]
	delete(r.trees, root)
	r.mu.Unlock()
	if entry == nil {
		return Trajectory{}, fmt.Errorf("%w: root recording is absent or already consumed", ErrIncompleteRecording)
	}
	entry.mu.Lock()
	entry.sealed = true
	events, models, observations, startedAt, recordingErr := entry.events, entry.models, entry.tools, entry.startedAt, entry.err
	entry.mu.Unlock()
	if recordingErr != nil {
		return Trajectory{}, recordingErr
	}
	var calls []ToolCall
	for _, observation := range observations {
		if observation.invalid || !observation.started || !observation.settled || !observation.call.Outcome.Valid() {
			return Trajectory{}, fmt.Errorf("%w: tool %q lacks paired observations", ErrIncompleteRecording, observation.call.Call.Name)
		}
		calls = append(calls, observation.call)
	}
	var output *agent.Output
	if value, ok := result.Output(); ok {
		output = &value
	}
	var elapsed *time.Duration
	if !startedAt.IsZero() {
		elapsed = new(time.Since(startedAt))
	}
	return New(Config{
		RootProcessID: root, Termination: result.Termination(), Output: output,
		RootUsage: result.Usage(), Elapsed: elapsed, Coverage: coverage,
		Events: events, ModelCalls: models, ToolCalls: calls,
	})
}

// Discard releases one failed or abandoned recording. The Host must first join
// or stop its tree; later callbacks cannot implicitly reopen a consumed session.
func (r *Recorder) Discard(root agent.ProcessID) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.trees, root)
	r.mu.Unlock()
}

func recordedInvocation(invocation interaction.ToolInvocation) (toolIdentity, ToolCall) {
	incarnation, _ := invocation.TreeIncarnationID()
	identity := toolIdentity{invocation.Relation().ProcessID(), incarnation, invocation.EffectID()}
	call := ToolCall{
		ProcessID: identity.processID, TreeIncarnationID: incarnation, EffectID: identity.effectID,
		StepSequence: invocation.StepSequence(), ModelCall: invocation.ModelCallSequence(),
		Index: invocation.ToolCallIndex(), Call: invocation.ToolCall(),
	}
	return identity, call
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
