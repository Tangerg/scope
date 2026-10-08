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

// Admission can settle a tool without starting execution, so the start
// observation is independent of the settlement that determines Outcome.
type toolObservation struct {
	call    ToolCall
	started bool
}

// recordedEvidence is mutated only under its recording's lock and only until
// seal hands it to Take.
type recordedEvidence struct {
	events    []agent.Event
	models    map[attemptIdentity]ModelCall
	tools     map[attemptIdentity]toolObservation
	startedAt time.Time
	gaps      EvidenceGaps
}

func (r *recordedEvidence) callsFull() bool {
	return len(r.models)+len(r.tools) >= MaxRecordingCalls
}

func (r *recordedEvidence) appendEvent(event agent.Event) {
	if len(r.events) >= MaxRecordingEvents {
		incrementGap(&r.gaps.DroppedEvents)
		return
	}
	if event.Name() == agent.EventProcessRestored {
		r.startedAt = time.Time{}
	}
	r.events = append(r.events, event)
}

func (r *recordedEvidence) startModel(invocation interaction.ModelInvocation, request *chat.Request) {
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
	}
}

func (r *recordedEvidence) settleModel(invocation interaction.ModelInvocation, settlement interaction.ModelSettlement) {
	identity := modelAttempt(invocation)
	call, exists := r.models[identity]
	if !exists || call.Outcome() != ModelOutcomeUnobserved {
		incrementGap(&r.gaps.DroppedCallObservations)
		return
	}
	call.Response, call.Unknown, call.Failure = settlement.Response.Clone(), settlement.Unknown(), settlement.Failure
	if call.Outcome() == ModelOutcomeUnobserved || call.Validate() != nil {
		incrementGap(&r.gaps.DroppedCallObservations)
		return
	}
	r.models[identity] = call
}

func (r *recordedEvidence) startTool(invocation interaction.ToolInvocation) {
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

func (r *recordedEvidence) settleTool(invocation interaction.ToolInvocation, settlement interaction.ToolSettlement) {
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
	observed := make(map[agent.ProcessID]bool)
	for _, event := range r.events {
		observed[event.ProcessID()] = true
	}
	var models []ModelCall
	for _, call := range r.models {
		if r.retain(observed, call.ProcessID, call.Outcome() != ModelOutcomeUnobserved) {
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
func (r *recordedEvidence) retain(observed map[agent.ProcessID]bool, process agent.ProcessID, paired bool) bool {
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
	if r.startedAt.IsZero() {
		return nil
	}
	return new(time.Since(r.startedAt))
}

type recording struct {
	mu sync.Mutex
	// Callbacks that already found this session must not mutate exported evidence.
	sealed bool
	recordedEvidence
}

func newRecording(event agent.Event) *recording {
	entry := &recording{recordedEvidence: recordedEvidence{
		models: make(map[attemptIdentity]ModelCall),
		tools:  make(map[attemptIdentity]toolObservation),
	}}
	if event.Name() == agent.EventProcessStarted {
		entry.startedAt = time.Now()
	}
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

func (r *Recorder) open(event agent.Event) *recording {
	r.mu.Lock()
	defer r.mu.Unlock()
	root := event.Relation().RootID()
	if entry := r.trees[root]; entry != nil {
		return entry
	}
	opensSession := event.Relation().IsRoot() &&
		(event.Name() == agent.EventProcessStarted || event.Name() == agent.EventProcessRestored)
	if !opensSession || len(r.trees) >= MaxRecordingTrees {
		return nil
	}
	if r.trees == nil {
		r.trees = make(map[agent.ProcessID]*recording)
	}
	entry := newRecording(event)
	r.trees[root] = entry
	return entry
}

func (r *Recorder) detach(root agent.ProcessID) *recording {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.trees[root]
	delete(r.trees, root)
	return entry
}

func (r *Recorder) OnEvent(_ context.Context, event agent.Event) {
	if r == nil || !event.Valid() {
		return
	}
	if entry := r.open(event); entry != nil {
		entry.update(func(evidence *recordedEvidence) { evidence.appendEvent(event) })
	}
}

func (r *Recorder) OnModelStarted(_ context.Context, invocation interaction.ModelInvocation, request *chat.Request) {
	entry := r.session(invocation.Relation().RootID())
	if entry == nil || !invocation.Valid() || request == nil {
		return
	}
	entry.update(func(evidence *recordedEvidence) { evidence.startModel(invocation, request) })
}

func (r *Recorder) OnModelSettled(_ context.Context, invocation interaction.ModelInvocation, settlement interaction.ModelSettlement) {
	entry := r.session(invocation.Relation().RootID())
	if entry == nil || !invocation.Valid() {
		return
	}
	entry.update(func(evidence *recordedEvidence) { evidence.settleModel(invocation, settlement) })
}

func (r *Recorder) OnToolStarted(_ context.Context, invocation interaction.ToolInvocation) {
	entry := r.session(invocation.Relation().RootID())
	if entry == nil || !invocation.Valid() {
		return
	}
	entry.update(func(evidence *recordedEvidence) { evidence.startTool(invocation) })
}

func (r *Recorder) OnToolSettled(_ context.Context, invocation interaction.ToolInvocation, settlement interaction.ToolSettlement) {
	entry := r.session(invocation.Relation().RootID())
	if entry == nil || !invocation.Valid() {
		return
	}
	entry.update(func(evidence *recordedEvidence) { evidence.settleTool(invocation, settlement) })
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
	result, committed, err := settledRoot(ctx, process)
	if err != nil {
		return Trajectory{}, err
	}
	entry := r.detach(process.ID())
	if entry == nil {
		return Trajectory{}, fmt.Errorf("%w: root recording is absent or already consumed", ErrIncompleteRecording)
	}
	evidence := entry.seal()
	models, tools := evidence.calls()
	config := Config{
		RootProcessID: process.ID(), Elapsed: evidence.elapsed(), Coverage: coverage, Gaps: evidence.gaps,
		Events: evidence.events, ModelCalls: models, ToolCalls: tools,
	}
	if committed {
		config.Termination, config.RootUsage = result.Termination(), result.Usage()
	}
	return New(config)
}

// Discard releases one failed or abandoned recording. The Host must first join
// or stop its tree; later callbacks cannot implicitly reopen a consumed session.
func (r *Recorder) Discard(root agent.ProcessID) {
	if r == nil {
		return
	}
	r.detach(root)
}

// settledRoot reports committed=false for a RuntimeError, whose stop is
// already recorded as RuntimeStopped events rather than as a Result.
func settledRoot(ctx context.Context, process *agent.Process) (agent.Result, bool, error) {
	if err := process.Join(ctx); err != nil && !runtimeStopped(err) {
		return agent.Result{}, false, err
	}
	result, err := process.Await(ctx)
	switch {
	case err == nil:
		return result, true, nil
	case runtimeStopped(err):
		return agent.Result{}, false, nil
	default:
		return agent.Result{}, false, err
	}
}

func runtimeStopped(err error) bool {
	_, stopped := errors.AsType[*agent.RuntimeError](err)
	return stopped
}

func modelAttempt(invocation interaction.ModelInvocation) attemptIdentity {
	incarnation, _ := invocation.TreeIncarnationID()
	attempt, _ := invocation.AttemptID()
	return attemptIdentity{invocation.Relation().ProcessID(), incarnation, invocation.EffectID(), attempt}
}

func recordedInvocation(invocation interaction.ToolInvocation) (attemptIdentity, ToolCall) {
	incarnation, _ := invocation.TreeIncarnationID()
	attempt, _ := invocation.AttemptID()
	identity := attemptIdentity{invocation.Relation().ProcessID(), incarnation, invocation.EffectID(), attempt}
	call := ToolCall{
		ProcessID: identity.processID, TreeIncarnationID: incarnation, EffectID: identity.effectID, AttemptID: attempt,
		StepSequence: invocation.StepSequence(), Call: invocation.ToolCall(),
	}
	return identity, call
}
