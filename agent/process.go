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
	ErrProcessNotRunning     = errors.New("agent: process is not running")
	ErrEffectNotPending      = errors.New("agent: effect does not require resolution")
	ErrInvalidProcessControl = errors.New("agent: invalid process control request")
	errNilContext            = errors.New("agent: nil Context")
)

// A bounded buffer lets control-plane callers submit while the tree owner is
// completing a safe boundary without allowing an unbounded command backlog.
// Freeze management has its own bounded lane so a full Process queue cannot
// prevent the command that releases its barrier.
const treeCommandBufferCapacity = 32

// Process is an Engine-issued handle to one managed execution. Its fields and
// construction remain private so a caller cannot create a second lifecycle
// owner. Identity and allocation are immutable; [Engine.InspectTree] owns live inspection.
// Control methods submit requests to the owning tree runtime. Except for
// RequestCancellation, ctx bounds both submission and response waiting. Once
// a command enters the runtime queue, canceling ctx does not revoke it. A context
// already canceled before submission never admits a command.
type Process struct {
	handle *processHandleState
}

// ID returns the stable Process identity.
func (p *Process) ID() ProcessID {
	if p == nil || p.handle == nil {
		return ProcessID{}
	}
	return p.handle.processID
}

// DeploymentRef returns the exact Definition and dispatcher binding identity.
func (p *Process) DeploymentRef() DeploymentRef {
	if p == nil || p.handle == nil {
		return DeploymentRef{}
	}
	return p.handle.deploymentRef
}

// Relation returns the immutable parent/root/depth location assigned by the
// Engine. It is a root relation for Processes created through Engine.Start.
func (p *Process) Relation() ProcessRelation {
	if p == nil || p.handle == nil {
		return ProcessRelation{}
	}
	return p.handle.relation
}

// StartedAt returns the lifecycle time committed by its started outcome.
func (p *Process) StartedAt() time.Time {
	if p == nil || p.handle == nil {
		return time.Time{}
	}
	return p.handle.startedAt
}

// DeliverSignals submits one or more immutable Strategy inputs as an ordered,
// atomic batch. Unaddressed input queues for the next Strategy-safe Step,
// including while Paused or waiting for child completion. It never resumes
// either state by itself. An externally addressable wait requires an answer.
// An addressed answer while Waiting must name the current WaitID; any other
// external wait answer returns ErrSignalRejected. The batch is accepted or the
// mailbox remains unchanged. Reusing a SignalID with different normalized
// payload bytes or a different WaitID returns ErrSignalConflict. If any SignalID
// repeats with identical content, accepted is false with nil error and the
// whole batch is unchanged, including resource usage.
// In durable mode, accepted is true only after the mailbox and budget changes
// commit to the authoritative tree head. A caller timeout does not revoke an
// admitted command; retry the identical batch to reconcile uncertain delivery.
func (p *Process) DeliverSignals(ctx context.Context, requests ...SignalRequest) (accepted bool, err error) {
	if len(requests) == 0 {
		return false, ErrInvalidSignalRequest
	}
	owned := slices.Clone(requests)
	response, err := p.request(ctx, processCommand{kind: commandDeliverBatch, signalRequests: owned})
	return response.accepted, err
}

// Pause requests a scheduling pause at the next safe Step boundary. An
// in-flight Effect is allowed to settle before the pause becomes visible.
// A nil error acknowledges the local control intent, not its durable publication
// or completion; [Engine.InspectTree] reports StatusPaused only after a tree
// commit acknowledges the paused state in durable mode.
func (p *Process) Pause(ctx context.Context, reason string) error {
	_, err := p.request(ctx, processCommand{kind: commandPause, reason: reason})
	return err
}

// Resume makes an explicitly Paused Process schedulable again. External waits
// require an answer addressed to their WaitID; child waits require Framework
// child completion. Resume does not satisfy either wait.
// A nonterminal Process that is not Paused returns ErrInvalidProcessControl.
// A nil error acknowledges local resumption. Subsequent durable boundaries
// publish the resumed state before reporting their own acknowledgments.
func (p *Process) Resume(ctx context.Context) error {
	_, err := p.request(ctx, processCommand{kind: commandResume})
	return err
}

// RequestCancellation submits a caller-owned cancellation intent. A nil error
// means the request entered the owning tree runtime's queue; it does not mean the
// Process has reached a safe boundary or become terminal. Once submitted, ctx
// cancellation cannot revoke the request. The first committed cancellation
// intent maps to StatusCanceled with a host-cancellation cause.
// Applying the intent cancels owned Step, Dispatch, and child-admission contexts
// throughout the subtree before waiting for their results. Already started
// external work still settles; remaining planned Effects do not start. Required
// initialization and persistence acknowledgments retain independent contexts.
// A surviving parent receives the ordinary child-completion Signal
// and its Strategy decides how to continue. Await reports this Process's
// acknowledged terminal result; every descendant retains its own settlement.
func (p *Process) RequestCancellation(ctx context.Context, reason string) error {
	if p == nil || p.handle == nil {
		return ErrProcessNotRunning
	}
	ctx = requireContext(ctx)
	intent, err := newCancellationIntent(cancellationOwnerHost, reason)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidProcessControl, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime := p.handle.runtime.Load()
	if runtime == nil {
		return p.handle.closedRequestError()
	}
	if err := runtime.engine.observation.checkListenerReentrancy(ctx, p.handle.relation.RootID(), "RequestCancellation"); err != nil {
		return err
	}
	select {
	case runtime.commands <- newTreeProcessCommand(
		p.handle.processID,
		processCommand{kind: commandCancel, cancellationIntent: intent},
	):
		return nil
	case <-p.handle.outcomePublished:
		return p.handle.closedRequestError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Kill records the Engine control plane's highest-priority terminal intent.
// It signals owned work throughout the subtree and collects in-flight Effects
// before termination. It cannot force a goroutine or a remote operation to stop.
// A nil error acknowledges the local intent. Await establishes whether the
// resulting termination committed or this instance stopped with a RuntimeError.
func (p *Process) Kill(ctx context.Context, reason string) error {
	_, err := p.request(ctx, processCommand{kind: commandKill, reason: reason})
	return err
}

// ResolveUnknownEffect supplies a definite result after an Effect attempt became
// unknown. The Engine never converts unknown into retry or success implicitly.
// Terminal intent or a committed terminal result returns ErrProcessFinished;
// retained interrupted-batch evidence cannot resume a terminated execution.
func (p *Process) ResolveUnknownEffect(ctx context.Context, settlement Settlement) error {
	_, err := p.request(ctx, processCommand{kind: commandResolveUnknownEffect, settlement: settlement})
	return err
}

// Await waits for the immutable terminal result and the Engine's immediate
// parent/child bookkeeping for that termination. Canceling ctx stops only the
// wait; Process cancellation is explicit or follows the context passed to Start.
// A durability failure stops this instance and returns a RuntimeError with no
// Result. A failed logical execution returns a valid Result and nil error.
// Descendants may still be settling after this Process's result is ready.
func (p *Process) Await(ctx context.Context) (Result, error) {
	if p == nil || p.handle == nil {
		return Result{}, ErrProcessNotRunning
	}
	ctx = requireContext(ctx)
	if runtime := p.handle.runtime.Load(); runtime != nil {
		if err := runtime.engine.observation.checkListenerReentrancy(ctx, p.handle.relation.RootID(), "Await"); err != nil {
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
// and required acknowledgments in this runtime. It does not cancel execution
// or release registry entries. Canceling ctx stops only this wait.
// Ordinary execution failure and terminal Unknown settlements do not prevent
// a successful join; Await reports the Process result. A runtime failure in
// this subtree returns a RuntimeError after its local jobs have returned, even
// when this Process published its result before a descendant failed.
// Join makes no claim that a remote operation or a previous writer has stopped.
// Strategies wait without blocking a Dispatcher through WaitForChildren.
func (p *Process) Join(ctx context.Context) error {
	if p == nil || p.handle == nil {
		return ErrProcessNotRunning
	}
	ctx = requireContext(ctx)
	if runtime := p.handle.runtime.Load(); runtime != nil {
		if err := runtime.engine.observation.checkListenerReentrancy(ctx, p.handle.relation.RootID(), "Join"); err != nil {
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

func (p *Process) request(ctx context.Context, command processCommand) (processResponse, error) {
	if p == nil || p.handle == nil {
		return processResponse{}, ErrProcessNotRunning
	}
	ctx = requireContext(ctx)
	if err := ctx.Err(); err != nil {
		return processResponse{}, err
	}
	runtime := p.handle.runtime.Load()
	if runtime == nil {
		return processResponse{}, p.handle.closedRequestError()
	}
	if err := runtime.engine.observation.checkListenerReentrancy(ctx, p.handle.relation.RootID(), "process control"); err != nil {
		return processResponse{}, err
	}
	command.response = make(chan processResponse, 1)
	select {
	case runtime.commands <- newTreeProcessCommand(p.handle.processID, command):
	case <-p.handle.outcomePublished:
		return processResponse{}, p.handle.closedRequestError()
	case <-ctx.Done():
		return processResponse{}, ctx.Err()
	}
	select {
	case response := <-command.response:
		return response, response.err
	case <-p.handle.outcomePublished:
		select {
		case response := <-command.response:
			return response, response.err
		default:
			return processResponse{}, p.handle.closedRequestError()
		}
	case <-ctx.Done():
		return processResponse{}, ctx.Err()
	}
}

// Result is the immutable terminal outcome of one Process. A failed or
// canceled execution is represented by Termination, not by Await's error.
type Result struct {
	processID   ProcessID
	startedAt   time.Time
	finishedAt  time.Time
	output      Output
	termination Termination
	usage       Usage
}

// ProcessID returns the completed Process identity.
func (r Result) ProcessID() ProcessID { return r.processID }

// StartedAt returns the lifecycle start time.
func (r Result) StartedAt() time.Time { return r.startedAt }

// FinishedAt returns the committed terminal time.
func (r Result) FinishedAt() time.Time { return r.finishedAt }

// Status returns the terminal lifecycle state.
func (r Result) Status() Status { return r.termination.Status() }

// Termination returns the stable terminal cause and optional Failure.
func (r Result) Termination() Termination { return r.termination }

// Usage returns the final Framework-owned resource counters.
func (r Result) Usage() Usage { return r.usage }

// Output returns the final semantic result only for StatusCompleted.
func (r Result) Output() (Output, bool) { return r.output, r.output.Valid() }

func (r Result) Valid() bool {
	if !r.processID.Valid() || r.startedAt.IsZero() || r.finishedAt.Before(r.startedAt) || !r.termination.Valid() {
		return false
	}
	return r.termination.Status() == StatusCompleted && r.output.Valid() ||
		r.termination.Status() != StatusCompleted && !r.output.Valid()
}

// Budget returns the fixed non-renewable allocation assigned to this Process.
func (p *Process) Budget() Budget {
	if p == nil || p.handle == nil {
		return Budget{}
	}
	return p.handle.budget
}

// Capabilities returns the immutable authority set assigned to this Process.
func (p *Process) Capabilities() CapabilitySet {
	if p == nil || p.handle == nil {
		return CapabilitySet{}
	}
	return p.handle.capabilities
}

type commandKind uint8

const (
	commandInvalid commandKind = iota
	commandDeliverBatch
	commandPause
	commandResume
	commandCancel
	commandKill
	commandResolveUnknownEffect
	commandHostTerminated
)

type processCommand struct {
	kind               commandKind
	signalRequests     []SignalRequest
	settlement         Settlement
	hostErr            error
	cancellationIntent cancellationIntent
	reason             string
	response           chan processResponse
}

type processResponse struct {
	accepted bool
	err      error
}

func (p processCommand) reply(response processResponse) {
	if p.response == nil {
		return
	}
	p.response <- response
}

func requireContext(ctx context.Context) context.Context {
	if ctx == nil {
		panic(errNilContext)
	}
	return ctx
}
