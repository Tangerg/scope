package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

var (
	ErrProcessFinished       = errors.New("agent: process has finished")
	ErrEffectNotPending      = errors.New("agent: effect does not require resolution")
	ErrEffectReplayForbidden = errors.New("agent: effect cannot be replayed under the same identity")
	ErrEffectOutcomeUnknown  = errors.New("agent: effect outcome remains unknown")
	ErrInvalidProcessControl = errors.New("agent: invalid process control request")
	ErrNilContext            = errors.New("agent: nil Context")
)

// The capacity is an internal burst allowance, not a Process or Signal quota:
// full lanes backpressure callers through context-aware sends. Freeze commands
// use their own lane so a full Process queue cannot block releasing a barrier.
const treeCommandBufferCapacity = 32

// Process is an Engine-issued handle to one managed execution. Its fields and
// construction remain private so a caller cannot create a second lifecycle
// owner. Identity and allocation are immutable; [Engine.InspectTree] owns live inspection.
// The zero value and nil receiver are invalid; methods require an Engine-issued
// handle and may panic otherwise. A finished Process remains a valid handle.
// Control methods submit requests to the owning tree runtime. Except for
// RequestCancellation, ctx bounds both submission and response waiting. Once
// a command enters the runtime queue, canceling ctx does not revoke it. A context
// already canceled before submission never admits a command. If termination or
// runtime failure races a queued command, an error may replace its response even
// after the command took effect. Reconcile delivery through SignalReceipts and
// effect resolution through the Host's authoritative committer records. Retained
// snapshot Settlements expose current evidence, not a historical journal.
type Process struct {
	handle *processHandle
}

// ID returns the stable Process identity.
func (p *Process) ID() ProcessID {
	return p.handle.processID
}

func (p *Process) DeploymentRef() DeploymentRef {
	return p.handle.deploymentRef()
}

// Relation returns the immutable parent/root/depth location assigned by the
// Engine. It is a root relation for Processes created through Engine.Start.
func (p *Process) Relation() ProcessRelation {
	return p.handle.relation
}

// StartedAt returns the observed UTC lifecycle start time recorded before
// initialization, retained when the Process is published.
func (p *Process) StartedAt() time.Time {
	return p.handle.startedAt
}

// DeliverSignals submits one or more immutable Strategy inputs as an ordered,
// atomic batch: either every new identity is appended in request order or the
// mailbox remains unchanged. Accepted input queues for the next Strategy-safe
// Step, including while Paused or waiting for child completion.
//
// Unaddressed input must satisfy Descriptor.SignalSchema and never releases a
// wait or pause. A current external wait accepts only its answer until
// satisfied, including while Paused and after an earlier answer in the same
// batch; any other wait answer returns ErrSignalRejected. Reusing a SignalID
// with different normalized payload bytes or WaitID, or repeating it within the
// batch, returns ErrSignalConflict. Identical historical identities are
// retained without another budget charge or acceptance event. Exceeding
// mailbox, work-budget, Process snapshot, or tree snapshot capacity returns
// ErrResourceLimitExceeded.
//
// With nil error, accepted reports whether any new input committed to the
// authoritative tree head; false means every identity was already accepted. A
// caller timeout does not revoke an admitted command; retry the identical batch
// to reconcile uncertain delivery, or inspect SignalReceipts once the Process
// has terminated.
func (p *Process) DeliverSignals(ctx context.Context, requests ...SignalRequest) (accepted bool, err error) {
	ctx = RequireContext(ctx)
	if len(requests) == 0 {
		return false, ErrInvalidSignalRequest
	}
	owned := slices.Clone(requests)
	response, err := p.request(ctx, deliverSignalsRequest{requests: owned})
	return response.accepted, err
}

// Pause requests a scheduling pause for a Running or Waiting Process at the next
// safe Step boundary; a Paused Process returns ErrInvalidProcessControl. An
// in-flight Effect settles before the pause becomes visible. A committed wait
// keeps its WaitID and may queue answers, but only Resume releases the pause.
// An accepted pause discards an unadopted Wait candidate without consuming its
// Signals. A nil error acknowledges the local intent; [Engine.InspectTree]
// reports StatusPaused only after a tree commit acknowledges it.
func (p *Process) Pause(ctx context.Context, reason string) error {
	_, err := p.request(ctx, pauseRequest{reason: reason})
	return err
}

// Resume releases an explicit pause. An unanswered current wait returns to
// Waiting, since Resume satisfies no wait; otherwise the Process becomes
// Running. A nonterminal Process that is not Paused returns
// ErrInvalidProcessControl. A nil error acknowledges local resumption; later
// durable boundaries publish the resumed state.
func (p *Process) Resume(ctx context.Context) error {
	_, err := p.request(ctx, resumeRequest{})
	return err
}

// RequestCancellation submits a caller-owned cancellation intent. A nil error
// means the request entered the owning tree runtime's queue, not that the
// Process reached a safe boundary; ctx cancellation cannot revoke it. The first
// committed cancellation maps to StatusCanceled with a host-cancellation cause.
// An in-flight tree commit finishes before the intent applies, so storage
// latency also delays cancellation. Applying it cancels owned Step, Dispatch,
// and child-admission contexts throughout the subtree; started external work
// still settles, planned Effects do not start, and required acknowledgments
// keep independent contexts. A surviving parent receives the ordinary
// child-completion Signal.
func (p *Process) RequestCancellation(ctx context.Context, reason string) error {
	ctx = RequireContext(ctx)
	intent, err := newCancellationIntent(cancellationOwnerHost, reason)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidProcessControl, err)
	}
	return p.submit(ctx, cancelRequest{intent: intent}, nil)
}

// Kill records the Engine control plane's highest-priority terminal intent.
// It signals owned work throughout the subtree and collects in-flight Effects
// before termination. It cannot force a goroutine or a remote operation to stop.
// A nil error acknowledges the local intent. Await establishes whether the
// resulting termination committed or this instance stopped with a RuntimeError.
func (p *Process) Kill(ctx context.Context, reason string) error {
	_, err := p.request(ctx, killRequest{reason: reason})
	return err
}

// ResolveUnknownEffect supplies a definite result for effectID after its attempt
// became unknown. The Engine never converts unknown into retry or success implicitly.
// Acknowledged resolution publishes EventEffectResolved, not an attempt event.
// A definite result exceeding Process or tree snapshot capacity returns
// ErrResourceLimitExceeded without changing the Unknown record or durable head;
// the caller can then supply a smaller result.
// Terminal intent or a committed terminal result returns ErrProcessFinished;
// retained interrupted-batch evidence cannot resume a terminated execution.
func (p *Process) ResolveUnknownEffect(ctx context.Context, effectID EffectID, settlement Settlement) error {
	_, err := p.request(ctx, resolveUnknownEffectRequest{effectID: effectID, settlement: settlement})
	return err
}

// ReplayUnknownEffect explicitly repeats an uncertain Dispatcher Effect under
// its original identity and immutable intent when its ReplayPolicy is
// ReplayPolicySameIdentity. The Dispatcher must reconcile or idempotently
// repeat the external operation; replay does not claim the earlier attempt did
// no work.
//
// The Unknown remains authoritative until a definite settlement commits,
// including across crashes and cancellation. A nil error confirms that
// settlement; an uncertain attempt returns ErrEffectOutcomeUnknown. Every
// replay publishes its own attempt facts, and a committed definite result also
// publishes EventEffectResolved. Canceling ctx stops only the caller's wait.
// Terminal intent rejects new attempts, and concurrent replay or resolution
// returns ErrEffectNotPending.
func (p *Process) ReplayUnknownEffect(ctx context.Context, effectID EffectID) error {
	_, err := p.request(ctx, replayUnknownEffectRequest{effectID: effectID})
	return err
}

// Await waits for the immutable terminal result and the Engine's immediate
// parent/child bookkeeping for that termination. Canceling ctx stops only the
// wait; Process cancellation is explicit or follows the context passed to Start.
// A runtime failure stops this instance and returns a RuntimeError with no
// Result. A failed logical execution returns a valid Result and nil error.
// Descendants may still be settling after this Process's result is ready.
func (p *Process) Await(ctx context.Context) (Result, error) {
	ctx = RequireContext(ctx)
	if runtime := p.handle.treeRuntime(); runtime != nil {
		if err := runtime.checkEventListenerReentrancy(ctx, "Await"); err != nil {
			return Result{}, err
		}
	}
	select {
	case <-p.handle.bookkeepingDone:
		return p.handle.outcome()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// Join waits for this Process and every descendant to finish their owned work
// and required acknowledgments in this runtime. It neither cancels execution
// nor releases registry entries, and canceling ctx stops only this wait.
// Execution failure and terminal Unknown settlements still join successfully.
// A runtime failure in this subtree returns a RuntimeError after its local jobs
// return, even when this Process published its result first. Join makes no
// claim that a remote operation or a previous writer has stopped; Strategies
// wait on children through NewChildWaitEffect instead.
func (p *Process) Join(ctx context.Context) error {
	ctx = RequireContext(ctx)
	if runtime := p.handle.treeRuntime(); runtime != nil {
		if err := runtime.checkEventListenerReentrancy(ctx, "Join"); err != nil {
			return err
		}
	}
	select {
	case <-p.handle.joined:
		return p.handle.joinError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Process) request(ctx context.Context, request processRequest) (processResponse, error) {
	reply := make(processReply, 1)
	if err := p.submit(ctx, request, reply); err != nil {
		return processResponse{}, err
	}
	select {
	case response := <-reply:
		return response, response.err
	case <-p.handle.outcomePublished:
		select {
		case response := <-reply:
			return response, response.err
		default:
			return processResponse{}, p.handle.closedRequestError()
		}
	case <-ctx.Done():
		return processResponse{}, ctx.Err()
	}
}

// submit is the only path into the owning runtime's command lane; requests
// without a reply are accepted once they enter that lane.
func (p *Process) submit(ctx context.Context, request processRequest, reply processReply) error {
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime := p.handle.treeRuntime()
	if runtime == nil {
		return p.handle.closedRequestError()
	}
	if err := runtime.checkEventListenerReentrancy(ctx, "process control"); err != nil {
		return err
	}
	select {
	case runtime.processCommands <- processTreeCommand{processID: p.handle.processID, request: request, reply: reply}:
		return nil
	case <-p.handle.outcomePublished:
		return p.handle.closedRequestError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Result is the immutable terminal outcome of one Process. A failed or
// canceled execution is represented by Termination, not by Await's error.
type Result struct {
	processID   ProcessID
	startedAt   time.Time
	finishedAt  time.Time
	output      Payload
	termination Termination
	usage       Usage
}

func (r Result) ProcessID() ProcessID { return r.processID }

func (r Result) StartedAt() time.Time { return r.startedAt }

// FinishedAt returns the observed UTC time of committed termination. Wall-clock
// adjustments and restoration on another writer can make it earlier than StartedAt.
func (r Result) FinishedAt() time.Time { return r.finishedAt }

func (r Result) Status() Status { return r.termination.Status() }

func (r Result) Termination() Termination { return r.termination }

func (r Result) Usage() Usage { return r.usage }

// Output returns the final semantic result only for StatusCompleted.
func (r Result) Output() (Payload, bool) { return r.output, r.output.Valid() }

func (r Result) Valid() bool {
	if !r.processID.Valid() || r.startedAt.IsZero() || r.finishedAt.IsZero() || !r.termination.Valid() {
		return false
	}
	return (r.termination.Status() == StatusCompleted) == r.output.Valid()
}

func (r Result) wire() resultWire {
	return resultWire{
		ProcessID: r.processID, StartedAt: r.startedAt, FinishedAt: r.finishedAt,
		Output: r.output, Termination: r.termination, Usage: r.usage,
	}
}

func (p *Process) Budget() Budget {
	return p.handle.budget
}

func (p *Process) Capabilities() CapabilitySet {
	return p.handle.capabilities
}

// processRequest is sealed: each Process control operation carries exactly
// its own inputs to the tree owner.
type processRequest interface{ processRequest() }

type deliverSignalsRequest struct{ requests []SignalRequest }

func (deliverSignalsRequest) processRequest() {}

type pauseRequest struct{ reason string }

func (pauseRequest) processRequest() {}

type resumeRequest struct{}

func (resumeRequest) processRequest() {}

type cancelRequest struct{ intent cancellationIntent }

func (cancelRequest) processRequest() {}

type killRequest struct{ reason string }

func (killRequest) processRequest() {}

type resolveUnknownEffectRequest struct {
	effectID   EffectID
	settlement Settlement
}

func (resolveUnknownEffectRequest) processRequest() {}

type replayUnknownEffectRequest struct{ effectID EffectID }

func (replayUnknownEffectRequest) processRequest() {}

// hostTerminationRequest carries the root context's end into the tree owner.
type hostTerminationRequest struct{ cause error }

func (hostTerminationRequest) processRequest() {}

type processResponse struct {
	accepted bool
	err      error
}

// processReply answers one request. It must be buffered so the tree owner
// never waits for a caller that stopped listening. A nil reply belongs to a
// request whose submission is its acceptance.
type processReply chan processResponse

func (p processReply) send(response processResponse) {
	if p != nil {
		p <- response
	}
}

// RequireContext enforces the non-nil Context contract shared by Agent boundaries.
// It panics with ErrNilContext for a programming error and otherwise returns ctx.
func RequireContext(ctx context.Context) context.Context {
	if ctx == nil {
		panic(ErrNilContext)
	}
	return ctx
}
