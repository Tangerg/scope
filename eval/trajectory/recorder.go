package trajectory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
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

// Admission can settle a tool without starting execution, so the start
// observation is independent of the settlement that determines Outcome.
type toolObservation struct {
	call    ToolCall
	started bool
}

// recordedEvidence is mutated only under its recording's lock and only until
// seal hands it to Take.
type recordedEvidence struct {
	events    []run.Event
	models    map[attemptIdentity]ModelCall
	tools     map[attemptIdentity]toolObservation
	startedAt time.Time
	gaps      EvidenceGaps
}

func (r *recordedEvidence) callsFull() bool {
	return len(r.models)+len(r.tools) >= MaxRecordingCalls
}

func (r *recordedEvidence) appendEvent(event run.Event) {
	if len(r.events) >= MaxRecordingEvents {
		incrementGap(&r.gaps.DroppedEvents)
		return
	}
	if event.Relation().IsRoot() && event.Name() == run.EventProcessStarted {
		r.startedAt = event.OccurredAt()
	}
	if event.Name() == run.EventProcessRestored {
		r.startedAt = time.Time{}
	}
	r.events = append(r.events, event)
}

func (r *recordedEvidence) startModel(invocation agent.ModelInvocation, request *chat.Request) {
	identity := modelAttempt(invocation)
	_, exists := r.models[identity]
	if r.callsFull() || exists {
		incrementGap(&r.gaps.DroppedCallObservations)
		return
	}
	r.models[identity] = ModelCall{
		ProcessID: identity.processID, TreeIncarnationID: identity.incarnation,
		EffectID: identity.effectID, AttemptID: identity.attemptID, StepSequence: invocation.StepSequence(),
		CallSequence: invocation.ModelCallSequence(), Request: request.Clone(),
		Outcome: agent.ModelOutcomeUnobserved,
	}
}

func (r *recordedEvidence) settleModel(invocation agent.ModelInvocation, settlement agent.ModelSettlement) {
	identity := modelAttempt(invocation)
	call, exists := r.models[identity]
	if !exists || call.Outcome != agent.ModelOutcomeUnobserved {
		incrementGap(&r.gaps.DroppedCallObservations)
		return
	}
	if settlement.Validate() != nil {
		incrementGap(&r.gaps.DroppedCallObservations)
		return
	}
	call.Response, call.Outcome, call.Failure = settlement.Response.Clone(), settlement.Outcome, settlement.Failure
	if call.Validate() != nil {
		incrementGap(&r.gaps.DroppedCallObservations)
		return
	}
	r.models[identity] = call
}

func (r *recordedEvidence) startTool(invocation agent.ToolInvocation) {
	if r.callsFull() {
		incrementGap(&r.gaps.DroppedCallObservations)
		return
	}
	identity, call := recordedInvocation(invocation)
	observation, exists := r.tools[identity]
	if exists {
		incrementGap(&r.gaps.DroppedCallObservations)
		return
	}
	observation.call = call
	observation.started = true
	r.tools[identity] = observation
}

func (r *recordedEvidence) settleTool(invocation agent.ToolInvocation, settlement agent.ToolSettlement) {
	identity, call := recordedInvocation(invocation)
	observation, exists := r.tools[identity]
	if !exists && r.callsFull() || exists && observation.call.Outcome() != ToolOutcomeUnobserved {
		incrementGap(&r.gaps.DroppedCallObservations)
		return
	}
	if exists {
		call = observation.call
	}
	call.Result, call.InputRequired, call.Unknown, call.Failure = settlement.Result, settlement.InputRequired, settlement.Unknown(), settlement.Failure
	call.Evidence = settlement.Evidence
	if call.Outcome() == ToolOutcomeUnobserved || call.Validate() != nil {
		incrementGap(&r.gaps.DroppedCallObservations)
		return
	}
	observation.call = call.Clone()
	r.tools[identity] = observation
}

func (r *recordedEvidence) calls() ([]ModelCall, []ToolCall) {
	observed := make(map[run.ProcessID]bool)
	for _, event := range r.events {
		observed[event.Relation().ProcessID()] = true
	}
	var models []ModelCall
	for _, call := range r.models {
		if r.retain(observed, call.ProcessID, call.Outcome != agent.ModelOutcomeUnobserved) {
			models = append(models, call)
		}
	}
	var tools []ToolCall
	for _, observation := range r.tools {
		if r.retain(observed, observation.call.ProcessID, observation.started && observation.call.Outcome() != ToolOutcomeUnobserved) {
			tools = append(tools, observation.call)
		}
	}
	return models, tools
}

// retain drops a call whose Process lost every event: without a relation it
// cannot be attributed to this tree.
func (r *recordedEvidence) retain(observed map[run.ProcessID]bool, process run.ProcessID, paired bool) bool {
	if !observed[process] && r.gaps.DroppedEvents > 0 {
		incrementGap(&r.gaps.DroppedCallObservations)
		return false
	}
	if !paired {
		incrementGap(&r.gaps.UnpairedCalls)
	}
	return true
}

func (r *recordedEvidence) elapsed() *time.Duration {
	if r.startedAt.IsZero() || r.gaps.DroppedEvents > 0 {
		return nil
	}
	// Export scheduling and observer backlog are not execution latency.
	end := r.startedAt
	for _, e := range r.events {
		if e.OccurredAt().Before(r.startedAt) {
			return nil
		}
		if e.OccurredAt().After(end) {
			end = e.OccurredAt()
		}
	}
	return new(end.Sub(r.startedAt))
}

type recording struct {
	mu sync.Mutex
	// Callbacks that already found this session must not mutate exported evidence.
	sealed bool
	recordedEvidence
}

func newRecording() *recording {
	entry := &recording{recordedEvidence: recordedEvidence{
		models: make(map[attemptIdentity]ModelCall),
		tools:  make(map[attemptIdentity]toolObservation),
	}}

	return entry
}

func (r *recording) update(change func(*recordedEvidence)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.sealed {
		change(&r.recordedEvidence)
	}
}

func (r *recording) seal() recordedEvidence {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sealed = true
	return r.recordedEvidence
}

// Recorder's zero value accepts bounded concurrent tree activation recordings.
// Root process and tree incarnation identify a session. A root start, nonterminal restore, or
// valid dispatcher start opens one regardless of callback scheduling. Take joins the subtree, detaches the
// session, and validates outside shared locks. RuntimeStopped and known
// observation losses remain exportable evidence; they do not invent a Result.
type Recorder struct {
	mu    sync.Mutex
	trees map[activationProcess]*recording
}

// A dispatcher can observe its invocation before the bounded event lane has
// delivered ProcessStarted. Both sources address the same bounded session;
// settlement alone never opens a consumed session.
func (r *Recorder) session(root activationProcess, admit bool) *recording {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.trees[root]; entry != nil {
		return entry
	}
	if !admit || len(r.trees) >= MaxRecordingTrees {
		return nil
	}
	if r.trees == nil {
		r.trees = make(map[activationProcess]*recording)
	}
	entry := newRecording()
	r.trees[root] = entry
	return entry
}
func (r *Recorder) detach(root activationProcess) *recording {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.trees[root]
	delete(r.trees, root)
	return entry
}

func (r *Recorder) OnEvent(_ context.Context, event run.Event) {
	if r == nil || !event.Valid() {
		return
	}
	admit := event.Relation().IsRoot() && (event.Name() == run.EventProcessStarted || event.Name() == run.EventProcessRestored)
	if entry := r.session(activationProcess{event.Relation().RootID(), eventIncarnation(event)}, admit); entry != nil {
		entry.update(func(evidence *recordedEvidence) { evidence.appendEvent(event) })
	}
}

func (r *Recorder) OnModelStarted(_ context.Context, invocation agent.ModelInvocation, request *chat.Request) {
	if !invocation.Valid() || request == nil {
		return
	}
	incarnation, _ := invocation.TreeIncarnationID()
	entry := r.session(activationProcess{invocation.Relation().RootID(), incarnation}, true)
	if entry == nil {
		return
	}
	entry.update(func(evidence *recordedEvidence) { evidence.startModel(invocation, request) })
}

func (r *Recorder) OnModelSettled(_ context.Context, invocation agent.ModelInvocation, settlement agent.ModelSettlement) {
	if !invocation.Valid() {
		return
	}
	incarnation, _ := invocation.TreeIncarnationID()
	entry := r.session(activationProcess{invocation.Relation().RootID(), incarnation}, false)
	if entry == nil {
		return
	}
	entry.update(func(evidence *recordedEvidence) { evidence.settleModel(invocation, settlement) })
}

func (r *Recorder) OnToolStarted(_ context.Context, invocation agent.ToolInvocation) {
	if !invocation.Valid() {
		return
	}
	incarnation, _ := invocation.TreeIncarnationID()
	entry := r.session(activationProcess{invocation.Relation().RootID(), incarnation}, true)
	if entry == nil {
		return
	}
	entry.update(func(evidence *recordedEvidence) { evidence.startTool(invocation) })
}

func (r *Recorder) OnToolSettled(_ context.Context, invocation agent.ToolInvocation, settlement agent.ToolSettlement) {
	if !invocation.Valid() {
		return
	}
	incarnation, _ := invocation.TreeIncarnationID()
	entry := r.session(activationProcess{invocation.Relation().RootID(), incarnation}, false)
	if entry == nil {
		return
	}
	entry.update(func(evidence *recordedEvidence) { evidence.settleTool(invocation, settlement) })
}

// Take waits for root and descendant work and drains accepted observations
// before consuming evidence. Call it before releasing the root handle.
// Coverage is an exhaustive Host declaration; nil exports observations without
// asserting semantic completeness. A caller timeout preserves the session.
// RuntimeError is represented by recorded RuntimeStopped facts, not returned as
// an export failure. A previously committed root Result remains available;
// otherwise Termination is zero and no root output or usage is invented.
func (r *Recorder) Take(ctx context.Context, process *run.Handle[run.Payload], coverage *Coverage) (Trajectory, error) {
	if r == nil || process == nil || !process.Relation().IsRoot() {
		return Trajectory{}, fmt.Errorf("%w: root process is required", ErrIncompleteRecording)
	}
	coverage = coverage.clone()
	if coverage != nil {
		if err := coverage.Validate(); err != nil {
			return Trajectory{}, err
		}
	}
	result, committed, err := settledRoot(ctx, process)
	if err != nil {
		return Trajectory{}, err
	}
	published, err := process.FlushObservations(ctx)
	if err != nil {
		return Trajectory{}, err
	}
	entry := r.detach(activationProcess{process.Relation().ProcessID(), process.TreeIncarnationID()})
	if entry == nil {
		return Trajectory{}, fmt.Errorf("%w: root recording is absent or already consumed", ErrIncompleteRecording)
	}
	evidence := entry.seal()
	if len(evidence.events) == 0 {
		return Trajectory{}, fmt.Errorf("%w: no framework evidence for this activation", ErrIncompleteRecording)
	}
	// At subtree quiescence the stream owner's count includes every framework
	// event, including the trailing ones no callback could use to detect a gap.
	// The larger count subsumes the recorder's own retention-limit losses.
	if captured := uint64(len(evidence.events)); published > captured {
		evidence.gaps.DroppedEvents = max(evidence.gaps.DroppedEvents, published-captured)
	}
	models, tools := evidence.calls()
	config := Config{
		RootProcessID: process.Relation().ProcessID(), Elapsed: evidence.elapsed(), Coverage: coverage, Gaps: evidence.gaps,
		Events: evidence.events, ModelCalls: models, ToolCalls: tools,
	}
	if committed {
		config.Termination, config.RootUsage = result.Termination(), result.Usage()
	}
	return New(config)
}

// Discard drains accepted callbacks before releasing this activation. The Host
// must join the tree first. A deadline preserves evidence for a later attempt.
func (r *Recorder) Discard(ctx context.Context, process *run.Handle[run.Payload]) error {
	if r == nil || process == nil || !process.Relation().IsRoot() {
		return ErrIncompleteRecording
	}
	if _, err := process.FlushObservations(ctx); err != nil {
		return err
	}
	r.detach(activationProcess{process.Relation().ProcessID(), process.TreeIncarnationID()})
	return nil
}

// settledRoot reports committed=false for a RuntimeError, whose stop is
// already recorded as RuntimeStopped events rather than as a Result.
func settledRoot(ctx context.Context, process *run.Handle[run.Payload]) (run.Result[run.Payload], bool, error) {
	if err := process.Join(ctx); err != nil && !runtimeStopped(err) {
		return run.Result[run.Payload]{}, false, err
	}
	result, err := process.Await(ctx)
	switch {
	case err == nil:
		return result, true, nil
	case runtimeStopped(err):
		return run.Result[run.Payload]{}, false, nil
	default:
		return run.Result[run.Payload]{}, false, err
	}
}

func runtimeStopped(err error) bool {
	_, stopped := errors.AsType[*run.RuntimeError](err)
	return stopped
}

func modelAttempt(invocation agent.ModelInvocation) attemptIdentity {
	incarnation, _ := invocation.TreeIncarnationID()
	attempt, _ := invocation.AttemptID()
	return attemptIdentity{invocation.Relation().ProcessID(), incarnation, invocation.EffectID(), attempt}
}

func recordedInvocation(invocation agent.ToolInvocation) (attemptIdentity, ToolCall) {
	incarnation, _ := invocation.TreeIncarnationID()
	attempt, _ := invocation.AttemptID()
	identity := attemptIdentity{invocation.Relation().ProcessID(), incarnation, invocation.EffectID(), attempt}
	call := ToolCall{
		ProcessID: identity.processID, TreeIncarnationID: incarnation, EffectID: identity.effectID, AttemptID: attempt,
		StepSequence: invocation.StepSequence(), Call: invocation.ToolCall(),
	}
	return identity, call
}
