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

// Recording limits bound retained evidence. Overflow leaves an explicit gap;
// the exported fragment cannot certify complete history or semantic coverage.
const (
	MaxRecordingTrees  = 64
	MaxRecordingEvents = 65536
	MaxRecordingCalls  = 8192
)

type attemptIdentity struct {
	processID   agent.ProcessID
	incarnation agent.TreeIncarnationID
	effectID    agent.EffectID
	attemptID   agent.EffectAttemptID
}
type modelObservation struct {
	call    ModelCall
	started bool
	settled bool
}

type toolObservation struct {
	call    ToolCall
	started bool
	settled bool
}

type recording struct {
	// Callbacks that already found this session must not mutate exported evidence.
	sealed    bool
	mu        sync.Mutex
	events    []agent.Event
	models    map[attemptIdentity]modelObservation
	tools     map[attemptIdentity]toolObservation
	startedAt time.Time
	gaps      EvidenceGaps
}

// Recorder's zero value accepts bounded concurrent tree recordings. Only root
// start/restore events open a session. Take joins the subtree, detaches the
// session, and validates outside shared locks. RuntimeStopped and known
// observation losses remain exportable evidence; they do not invent a Result.
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
		entry = &recording{models: make(map[attemptIdentity]modelObservation), tools: make(map[attemptIdentity]toolObservation)}
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
		incrementGap(&entry.gaps.DroppedEvents)
		return
	}
	if event.Name() == agent.EventProcessRestored {
		entry.startedAt = time.Time{}
	}
	entry.events = append(entry.events, event)
}

func (r *Recorder) OnModelStarted(_ context.Context, invocation interaction.ModelInvocation, request *chat.Request) {
	entry := r.session(invocation.Relation().RootID())
	if entry == nil || !invocation.Valid() || request == nil {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.sealed {
		return
	}
	incarnation, _ := invocation.TreeIncarnationID()
	attempt, _ := invocation.AttemptID()
	identity := attemptIdentity{invocation.Relation().ProcessID(), incarnation, invocation.EffectID(), attempt}
	observation, exists := entry.models[identity]
	if !exists && len(entry.models)+len(entry.tools) >= MaxRecordingCalls || observation.started {
		incrementGap(&entry.gaps.DroppedCallObservations)
		return
	}
	observation.call = ModelCall{
		ProcessID: identity.processID, TreeIncarnationID: incarnation,
		EffectID: identity.effectID, AttemptID: attempt, StepSequence: invocation.StepSequence(),
		CallSequence: invocation.ModelCallSequence(), Request: request.Clone(), Outcome: ModelOutcomeUnobserved,
	}
	observation.started = true
	entry.models[identity] = observation
}

func (r *Recorder) OnModelSettled(_ context.Context, invocation interaction.ModelInvocation, settlement interaction.ModelSettlement) {
	entry := r.session(invocation.Relation().RootID())
	if entry == nil || !invocation.Valid() {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.sealed {
		return
	}
	incarnation, _ := invocation.TreeIncarnationID()
	attempt, _ := invocation.AttemptID()
	identity := attemptIdentity{invocation.Relation().ProcessID(), incarnation, invocation.EffectID(), attempt}
	observation, exists := entry.models[identity]
	if !exists || observation.settled {
		incrementGap(&entry.gaps.DroppedCallObservations)
		return
	}
	switch {
	case settlement.Response != nil && !settlement.Unknown && settlement.Failure == "":
		observation.call.Outcome = ModelOutcomeSucceeded
		observation.call.Response = settlement.Response.Clone()
	case settlement.Response == nil && settlement.Unknown:
		observation.call.Outcome = ModelOutcomeUnknown
		observation.call.Failure = settlement.Failure
	default:
		incrementGap(&entry.gaps.DroppedCallObservations)
		return
	}
	observation.settled = true
	entry.models[identity] = observation
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
		incrementGap(&entry.gaps.DroppedCallObservations)
		return
	}
	identity, call := recordedInvocation(invocation)
	observation := entry.tools[identity]
	if observation.started {
		incrementGap(&entry.gaps.DroppedCallObservations)
		return
	}
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
		incrementGap(&entry.gaps.DroppedCallObservations)
		return
	}
	if observation.settled {
		incrementGap(&entry.gaps.DroppedCallObservations)
		return
	}
	if !observation.started {
		observation.call = call
	}
	observation.call.Outcome, observation.call.Result, observation.call.Failure = recordedOutcome(settlement)
	if settlement.Evidence != nil {
		observation.call.Evidence = new(settlement.Evidence.Clone())
	}
	observation.settled = true
	entry.tools[identity] = observation
}

// Take waits for root and descendant work to settle before consuming evidence.
// Coverage is an exhaustive Host declaration; nil exports observations without
// asserting semantic completeness. A caller timeout preserves the session.
// RuntimeError is represented by recorded RuntimeStopped facts, not returned as
// an export failure. A previously committed root Result remains available;
// otherwise Termination is zero and no root output or usage is invented.
func (r *Recorder) Take(ctx context.Context, process *agent.Process, coverage *Coverage) (Trajectory, error) {
	if r == nil || process == nil || !process.Relation().IsRoot() {
		return Trajectory{}, fmt.Errorf("%w: root process is required", ErrIncompleteRecording)
	}
	coverage = coverage.clone()
	if coverage != nil {
		if err := coverage.Validate(); err != nil {
			return Trajectory{}, err
		}
	}
	if err := process.Join(ctx); err != nil {
		if _, stopped := errors.AsType[*agent.RuntimeError](err); !stopped {
			return Trajectory{}, err
		}
	}
	result, err := process.Await(ctx)
	if err != nil {
		if _, stopped := errors.AsType[*agent.RuntimeError](err); !stopped {
			return Trajectory{}, err
		}
	}
	root := process.ID()
	r.mu.Lock()
	entry := r.trees[root]
	delete(r.trees, root)
	r.mu.Unlock()
	if entry == nil {
		return Trajectory{}, fmt.Errorf("%w: root recording is absent or already consumed", ErrIncompleteRecording)
	}
	entry.mu.Lock()
	entry.sealed = true
	events, modelObservations, observations, startedAt, gaps := entry.events, entry.models, entry.tools, entry.startedAt, entry.gaps
	entry.mu.Unlock()
	observedProcesses := make(map[agent.ProcessID]bool)
	for _, event := range events {
		observedProcesses[event.ProcessID()] = true
	}
	var models []ModelCall
	for _, observation := range modelObservations {
		if !observedProcesses[observation.call.ProcessID] && gaps.DroppedEvents > 0 {
			incrementGap(&gaps.DroppedCallObservations)
			continue
		}
		if !observation.started || !observation.settled {
			incrementGap(&gaps.UnpairedCalls)
		}
		models = append(models, observation.call)
	}
	var calls []ToolCall
	for _, observation := range observations {
		if !observedProcesses[observation.call.ProcessID] && gaps.DroppedEvents > 0 {
			incrementGap(&gaps.DroppedCallObservations)
			continue
		}
		if !observation.started || !observation.settled || !observation.call.Outcome.Valid() {
			incrementGap(&gaps.UnpairedCalls)
			if !observation.call.Outcome.Valid() {
				observation.call.Outcome = ToolOutcomeUnobserved
			}
		}
		calls = append(calls, observation.call)
	}
	var output agent.Payload
	var termination agent.Termination
	var usage agent.Usage
	if err == nil {
		output, _ = result.Output()
		termination, usage = result.Termination(), result.Usage()
	}
	var elapsed *time.Duration
	if !startedAt.IsZero() {
		elapsed = new(time.Since(startedAt))
	}
	return New(Config{
		RootProcessID: root, Termination: termination, Output: output,
		RootUsage: usage, Elapsed: elapsed, Coverage: coverage, Gaps: gaps,
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

func recordedInvocation(invocation interaction.ToolInvocation) (attemptIdentity, ToolCall) {
	incarnation, _ := invocation.TreeIncarnationID()
	attempt, _ := invocation.AttemptID()
	identity := attemptIdentity{invocation.Relation().ProcessID(), incarnation, invocation.EffectID(), attempt}
	call := ToolCall{
		ProcessID: identity.processID, TreeIncarnationID: incarnation, EffectID: identity.effectID, AttemptID: attempt,
		StepSequence: invocation.StepSequence(), ModelCall: invocation.ModelCallSequence(),
		Index: invocation.ToolCallIndex(), Call: invocation.ToolCall(), Outcome: ToolOutcomeUnobserved,
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
