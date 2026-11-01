package run

import (
	"context"
	"errors"
	"fmt"
	"iter"
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

// Process is a Runtime-issued handle to one managed execution. Its fields and
// construction remain private so a caller cannot create a second lifecycle
// owner. Identity and allocation are immutable; [Runtime.InspectTree] owns live inspection.
// The zero value and nil receiver are invalid; methods require a Runtime-issued
// handle and may panic otherwise. A finished Process remains a valid handle.
// Control methods submit requests to the owning tree runtime. The context bounds both submission and response waiting. Once
// a command enters the runtime queue, canceling ctx does not revoke it. A context
// already canceled before submission never admits a command. If termination or
// runtime failure races a queued command, an error may replace its response even
// after the command took effect. Reconcile delivery through SignalReceipts and
// effect resolution through the Host's authoritative committer records. Retained
// snapshot Settlements expose current evidence, not a historical journal.
type Handle[O any] struct {
	_      [0]*O
	handle *processHandle
}

func (h *Handle[O]) ProgramRef() ProgramRef {
	return h.handle.programRef
}

// Relation returns the Process identity with its immutable parent/root/depth
// location assigned by the Runtime. It is a root relation for Processes created
// through Runtime.Start.
func (h *Handle[O]) Relation() ProcessRelation {
	return h.handle.relation
}

// TreeIncarnationID identifies this activation, including after its runtime exits.
func (h *Handle[O]) TreeIncarnationID() TreeIncarnationID { return h.handle.incarnationID }

// StartedAt returns the observed UTC lifecycle start time stamped when the
// Process is first prepared for publication, after admission and initialization,
// and retained unchanged across recovery.
func (h *Handle[O]) StartedAt() time.Time {
	return h.handle.startedAt
}

// DeliverSignals submits one or more immutable Strategy inputs as an ordered,
// atomic batch: either every new identity is appended in request order or the
// mailbox remains unchanged. Accepted input queues for the next Strategy-safe
// Step, including while Paused or waiting for child completion.
//
// Unaddressed input must satisfy Descriptor.SignalSchema and never releases a
// wait or pause. A current external wait accepts only its answer until
// satisfied, including while Paused. Later unaddressed input in the same batch
// is then admitted; an answer to any other wait returns ErrSignalRejected.
// Reusing a SignalID
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
func (h *Handle[O]) DeliverSignals(ctx context.Context, requests ...SignalRequest) (accepted bool, err error) {
	ctx = RequireContext(ctx)
	if len(requests) == 0 {
		return false, ErrInvalidSignalRequest
	}
	owned := slices.Clone(requests)
	response, err := h.request(ctx, deliverSignalsRequest{requests: owned})
	return response.accepted, err
}

// Pause requests a scheduling pause for a Running or Waiting Process at the next
// safe Step boundary; a Paused Process returns ErrInvalidProcessControl. An
// in-flight Effect settles before the pause becomes visible. A committed wait
// keeps its WaitID and may queue answers, but only Resume releases the pause.
// An accepted pause discards an unadopted Wait candidate without consuming its
// Signals. A nil error acknowledges the local intent; [Runtime.InspectTree]
// reports StatusPaused only after a tree commit acknowledges it.
func (h *Handle[O]) Pause(ctx context.Context, reason string) error {
	_, err := h.request(ctx, pauseRequest{reason: reason})
	return err
}

// Resume releases an explicit pause. An unanswered current wait returns to
// Waiting, since Resume satisfies no wait; otherwise the Process becomes
// Running. A nonterminal Process that is not Paused returns
// ErrInvalidProcessControl. A nil error acknowledges local resumption; later
// durable boundaries publish the resumed state.
func (h *Handle[O]) Resume(ctx context.Context) error {
	_, err := h.request(ctx, resumeRequest{})
	return err
}

// Cancel records a terminal intent on the authoritative head. Acknowledgment
// does not mean the local subtree joined or an external operation stopped.
// A caller timeout cannot revoke an admitted request.
func (h *Handle[O]) Cancel(ctx context.Context, reason string) error {
	ctx = RequireContext(ctx)
	intent, err := newCancellationIntent(cancellationOwnerHost, reason)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidProcessControl, err)
	}
	tree := h.handle.treeRuntime()
	if tree == nil {
		return h.handle.closedRequestError()
	}
	reply := make(processReply, 1)
	if err = h.submit(ctx, cancelRequest{intent: intent}, reply); err != nil {
		return err
	}
	select {
	case response := <-reply:
		return response.err
	case <-tree.done:
		select {
		case response := <-reply:
			return response.err
		default:
			return h.handle.closedRequestError()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Answer admits a typed reply to an external wait in this handle's subtree.
// One WaitID has one answer identity. Matching historical receipts reconcile
// retries after the wait closes; another normalized answer conflicts.
func (h *Handle[O]) Answer[A any](ctx context.Context, wait WaitID, value A) error {
	ctx = RequireContext(ctx)
	if !wait.Valid() {
		return ErrSignalRejected
	}
	payload, err := EncodePayload(value)
	if err != nil {
		return err
	}
	req, err := answerRequest(wait, payload)
	if err != nil {
		return err
	}
	tree := h.handle.treeRuntime()
	if tree == nil {
		return h.handle.closedRequestError()
	}
	locate := func() (ProcessID, bool, error) {
		view, inspectTreeErr := tree.engine.InspectTree(ctx, h.Relation().RootID())
		if inspectTreeErr != nil {
			return ProcessID{}, false, inspectTreeErr
		}
		parents := make(map[ProcessID]ProcessID, len(view.Processes))
		for _, p := range view.Processes {
			rel := p.Snapshot.Relation()
			parent, _ := rel.ParentID()
			parents[rel.ProcessID()] = parent
		}
		for _, p := range view.Processes {
			target := p.Snapshot.Relation().ProcessID()
			ancestor := target
			for ancestor.Valid() && ancestor != h.ID() {
				ancestor = parents[ancestor]
			}
			if ancestor != h.ID() {
				continue
			}
			if found, receiptErr := p.Snapshot.answered(wait, ComputeDigest(req.payload)); found || receiptErr != nil {
				return target, found, receiptErr
			}
			if current, open := p.Snapshot.WaitID(); open && current == wait {
				if kind, _ := p.Snapshot.WaitKind(); kind != WaitKindExternal {
					return ProcessID{}, false, ErrSignalRejected
				}
				return target, false, nil
			}
		}
		return ProcessID{}, false, ErrSignalRejected
	}
	target, acknowledged, err := locate()
	if err != nil || acknowledged {
		return err
	}
	owner, found := tree.engine.Process(target)
	if !found {
		return ErrTreeNotFound
	}
	_, err = owner.DeliverSignals(ctx, req)
	if errors.Is(err, ErrProcessFinished) || errors.Is(err, ErrSignalRejected) {
		if _, accepted, reconcileErr := locate(); accepted || errors.Is(reconcileErr, ErrSignalConflict) {
			return reconcileErr
		}
	}
	return err
}

// Kill records the Runtime control plane's highest-priority terminal intent.
// It signals owned work throughout the subtree and collects in-flight Effects
// before termination. It cannot force a goroutine or a remote operation to stop.
// A nil error acknowledges the local intent. Await establishes whether the
// resulting termination committed or this instance stopped with a RuntimeError.
func (h *Handle[O]) Kill(ctx context.Context, reason string) error {
	_, err := h.request(ctx, killRequest{reason: reason})
	return err
}

// ResolveUnknownEffect supplies a definite result for effectID after its attempt
// became unknown. The Runtime never converts unknown into retry or success implicitly.
// Acknowledged resolution publishes EventEffectResolved, not an attempt event.
// A definite result exceeding Process or tree snapshot capacity returns
// ErrResourceLimitExceeded without changing the Unknown record or durable head;
// the caller can then supply a smaller result.
// Terminal intent or a committed terminal result returns ErrProcessFinished;
// retained interrupted-batch evidence cannot resume a terminated execution.
func (h *Handle[O]) ResolveUnknownEffect(ctx context.Context, effectID EffectID, settlement Settlement) error {
	_, err := h.request(ctx, resolveUnknownEffectRequest{effectID: effectID, settlement: settlement})
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
func (h *Handle[O]) ReplayUnknownEffect(ctx context.Context, effectID EffectID) error {
	_, err := h.request(ctx, replayUnknownEffectRequest{effectID: effectID})
	return err
}

// Await waits for the immutable terminal result and the Runtime's immediate
// parent/child bookkeeping for that termination. Canceling ctx stops only the
// wait; Process cancellation is explicit or follows the context passed to Start.
// A runtime failure stops this instance and returns a RuntimeError with no
// Result. A failed logical execution returns a valid Result and nil error.
// Descendants may still be settling after this Process's result is ready.
func (h *Handle[O]) Await(ctx context.Context) (Result[O], error) {
	ctx = RequireContext(ctx)
	if runtime := h.handle.treeRuntime(); runtime != nil {
		if err := runtime.checkEventListenerReentrancy(ctx, "Await"); err != nil {
			return Result[O]{}, err
		}
	}
	select {
	case <-h.handle.bookkeepingDone:
		result, err := h.handle.outcome()
		return projectResult[O](result), err
	case <-ctx.Done():
		return Result[O]{}, ctx.Err()
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
func (h *Handle[O]) Join(ctx context.Context) error {
	ctx = RequireContext(ctx)
	if runtime := h.handle.treeRuntime(); runtime != nil {
		if err := runtime.checkEventListenerReentrancy(ctx, "Join"); err != nil {
			return err
		}
	}
	select {
	case <-h.handle.joined:
		return h.handle.joinError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Handle[O]) request(ctx context.Context, request processRequest) (processResponse, error) {
	reply := make(processReply, 1)
	if err := h.submit(ctx, request, reply); err != nil {
		return processResponse{}, err
	}
	select {
	case response := <-reply:
		return response, response.err
	case <-h.handle.outcomePublished:
		select {
		case response := <-reply:
			return response, response.err
		default:
			return processResponse{}, h.handle.closedRequestError()
		}
	case <-ctx.Done():
		return processResponse{}, ctx.Err()
	}
}

// submit is the only path into the owning runtime's command lanes; requests
// without a reply are accepted once they enter that lane.
func (h *Handle[O]) submit(ctx context.Context, request processRequest, reply processReply) error {
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime := h.handle.treeRuntime()
	if runtime == nil {
		return h.handle.closedRequestError()
	}
	if err := runtime.checkEventListenerReentrancy(ctx, "process control"); err != nil {
		return err
	}
	commands := runtime.controlCommands
	if _, signal := request.(deliverSignalsRequest); signal {
		commands = runtime.signalCommands
	}
	select {
	case commands <- processTreeCommand{processID: h.handle.processID(), request: request, reply: reply}:
		return nil
	case <-h.handle.outcomePublished:
		return h.handle.closedRequestError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Result is the immutable terminal outcome of one Process. A failed or
// canceled execution is represented by Termination, not by Await's error.
type Result[O any] struct {
	_           [0]*O
	processID   ProcessID
	startedAt   time.Time
	finishedAt  time.Time
	termination Termination
	usage       Usage
}

func projectResult[O any](value Result[Payload]) Result[O] {
	return Result[O]{processID: value.processID, startedAt: value.startedAt, finishedAt: value.finishedAt, termination: value.termination, usage: value.usage}
}

func (h *Handle[O]) ID() ProcessID { return h.handle.processID() }

// Output projects an immutable terminal result, preserving a logical failure as
// such rather than manufacturing an infrastructure or transport failure.
func (r Result[O]) Output() (O, error) {
	var zero O
	if !r.Valid() {
		return zero, errors.New("run: no terminal result")
	}
	if output, ok := r.termination.Output(); ok {
		return output.Decode[O]()
	}
	return zero, &TerminationError{ID: r.processID, Termination: r.termination}
}

type TerminationError struct {
	ID          ProcessID
	Termination Termination
}

func (t *TerminationError) Error() string {
	return fmt.Sprintf("run %s: %s", t.ID, t.Termination.Status())
}

func (r Result[O]) ProcessID() ProcessID { return r.processID }

func (r Result[O]) StartedAt() time.Time { return r.startedAt }

// FinishedAt returns the observed UTC time of committed termination. Wall-clock
// adjustments and restoration on another writer can make it earlier than StartedAt.
func (r Result[O]) FinishedAt() time.Time { return r.finishedAt }

func (r Result[O]) Termination() Termination { return r.termination }

func (r Result[O]) Usage() Usage { return r.usage }

func (r Result[O]) Valid() bool {
	return r.processID.Valid() && !r.startedAt.IsZero() && !r.finishedAt.IsZero() && r.termination.Valid()
}

func (r Result[O]) wire() resultWire {
	return resultWire{
		ProcessID: r.processID, StartedAt: r.startedAt, FinishedAt: r.finishedAt,
		Termination: r.termination, Usage: r.usage,
	}
}

func (h *Handle[O]) Budget() Budget {
	return h.handle.budget
}

func (h *Handle[O]) Capabilities() CapabilitySet {
	return h.handle.capabilities
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

// FlushObservations captures the number of framework events published in this
// activation and joins callbacks already accepted on its owning runtime. Use
// it after Join and before ReleaseTree when exporting evidence: a shorter
// recording then reveals exact loss, including a missing terminal event.
// Flushing itself does not repair drops.
func (h *Handle[O]) FlushObservations(ctx context.Context) (published uint64, err error) {
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if h == nil || h.handle == nil {
		return 0, ErrTreeNotFound
	}
	tree := h.handle.treeRuntime()
	if tree == nil {
		return 0, ErrTreeNotFound
	}
	bus := tree.engine.observation
	if err := bus.checkListenerJoin(ctx, "FlushObservations"); err != nil {
		return 0, err
	}
	count := tree.writer.stream.publishedEvents()
	if err := bus.flush(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

// Observe follows this Handle's whole tree. A zero cursor atomically reads the
// acknowledged snapshot and attaches a bounded subscription. An exact cursor
// continues without re-emitting the snapshot. A stale cursor or an overflowing
// subscriber returns ErrObservationGap; restart with a zero cursor to resync.
// Observation cannot start, cancel, or take over execution. Breaking iteration
// or canceling ctx detaches this subscriber only.
func (h *Handle[O]) Observe(ctx context.Context, after Cursor, capacity int) iter.Seq2[Observation, error] {
	return func(yield func(Observation, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(Observation{}, err)
			return
		}
		if capacity < 1 {
			yield(Observation{}, ErrObservationCapacity)
			return
		}
		if h == nil || h.handle == nil {
			yield(Observation{}, ErrTreeNotFound)
			return
		}
		tree := h.handle.treeRuntime()
		if tree == nil {
			yield(Observation{}, ErrTreeNotFound)
			return
		}
		stream := &tree.writer.stream
		view, r, err := stream.attach(after, capacity)
		if err != nil {
			yield(Observation{}, err)
			return
		}
		defer stream.detach(r)
		if after == (Cursor{}) && !yield(view, nil) {
			return
		}
		for {
			select {
			case <-ctx.Done():
				yield(Observation{}, ctx.Err())
				return
			case change, ok := <-r.queue:
				if !ok {
					if r.gap {
						yield(Observation{}, ErrObservationGap)
					}
					return
				}
				if !yield(change, nil) {
					return
				}
			}
		}
	}
}
