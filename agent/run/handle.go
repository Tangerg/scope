package run

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"
)

const observationPollInterval = 10 * time.Millisecond

type Handle[O any] struct {
	runtime *Runtime
	tree    *treeRuntime
	process *processState
	id      ProcessID
	program ProgramRef
	epoch   Epoch
}

func typedHandle[O any](h *Handle[Payload]) *Handle[O] {
	return &Handle[O]{runtime: h.runtime, tree: h.tree, process: h.process, id: h.id, program: h.program, epoch: h.epoch}
}

func (h *Handle[O]) ID() ProcessID          { return h.id }
func (h *Handle[O]) ProgramRef() ProgramRef { return h.program }
func (h *Handle[O]) Epoch() Epoch           { return h.epoch }
func (h *Handle[O]) Inspect(ctx context.Context) (TreeInspection, error) {
	return h.runtime.Inspect(ctx, h.id.Root())
}

func (h *Handle[O]) DeliverSignals(ctx context.Context, signals ...Signal) (bool, error) {
	if err := RequireContext(ctx).Err(); err != nil {
		return false, err
	}
	if h.admissionClosed() {
		err := h.committedSignals(ctx, signals)
		if err != nil && h.tree != nil {
			h.tree.mu.RLock()
			failed := h.tree.failure != nil
			h.tree.mu.RUnlock()
			if failed {
				err = errors.Join(err, h.tree.failureError())
			}
		}
		return false, err
	}
	response, err := h.request(ctx, h.id, signalCommand{signals: append([]Signal(nil), signals...)})
	if err != nil && ctx.Err() == nil && h.admissionClosed() {
		if confirmation := h.committedSignals(ctx, signals); confirmation != nil {
			return false, errors.Join(err, confirmation)
		}
		return false, nil
	}
	return response.accepted, err
}

func (h *Handle[O]) admissionClosed() bool {
	if h.tree == nil {
		return true
	}
	h.tree.mu.RLock()
	process := h.tree.state.processes[h.id]
	finished := process == nil || process.exited != nil
	h.tree.mu.RUnlock()
	if finished {
		return true
	}
	select {
	case <-h.tree.done:
		return true
	default:
		return false
	}
}

// A stopped writer can acknowledge an exact retry from committed facts. It has
// no authority to admit another input, even if another writer has recovered it.
func (h *Handle[O]) committedSignals(ctx context.Context, signals []Signal) error {
	receipts, exited, err := h.runtime.receipts(ctx, h.id)
	if err != nil {
		return err
	}
	fresh, err := signalBatch(signals).unadmitted(receipts)
	if err != nil || len(fresh) == 0 {
		return err
	}
	if exited {
		return ErrProcessFinished
	}
	return ErrProcessNotActive
}

func (h *Handle[O]) Answer[A any](ctx context.Context, wait WaitKey, value A) error {
	if !wait.Valid() {
		return ErrSignalRejected
	}
	payload, err := EncodePayload(value)
	if err != nil {
		return err
	}
	_, err = h.DeliverSignals(ctx, answerSignal(wait, payload))
	return err
}

func answerSignal(wait WaitKey, payload Payload) Signal {
	return Signal{ID: SignalID{identity{value: ComputeDigest([]byte(wait.String())).hex()}}, Wait: wait, Payload: payload}
}

func (h *Handle[O]) Pause(ctx context.Context, reason string) error {
	_, err := h.request(ctx, h.id, controlCommand{kind: ControlPause, reason: reason})
	return err
}

func (h *Handle[O]) Resume(ctx context.Context) error {
	_, err := h.request(ctx, h.id, controlCommand{kind: ControlResume})
	return err
}

func (h *Handle[O]) Cancel(ctx context.Context, reason string) error {
	_, err := h.request(ctx, h.id, controlCommand{kind: ControlCancel, reason: reason})
	return err
}

func (h *Handle[O]) Kill(ctx context.Context, reason string) error {
	_, err := h.request(ctx, h.id, controlCommand{kind: ControlKill, reason: reason})
	return err
}

func (h *Handle[O]) ResolveUnknownEffect(ctx context.Context, id EffectID, outcome Settlement) error {
	if !h.id.Contains(id.Process) {
		return ErrEffectNotPending
	}
	_, err := h.request(ctx, id.Process, resolveCommand{id: id, outcome: outcome})
	return err
}

func (h *Handle[O]) ReplayUnknownEffect(ctx context.Context, id EffectID) error {
	if !h.id.Contains(id.Process) {
		return ErrEffectNotPending
	}
	_, err := h.request(ctx, id.Process, replayCommand{id: id})
	return err
}

func (h *Handle[O]) Await(ctx context.Context) (Result[O], error) {
	ctx = RequireContext(ctx)
	if h.tree != nil {
		return h.awaitLocal(ctx)
	}
	return h.awaitCommitted(ctx)
}

func (h *Handle[O]) awaitLocal(ctx context.Context) (Result[O], error) {
	select {
	case <-h.process.done:
	case <-h.tree.done:
	case <-ctx.Done():
		return Result[O]{}, ctx.Err()
	}
	h.tree.mu.RLock()
	defer h.tree.mu.RUnlock()
	if h.process.exited == nil {
		return Result[O]{}, h.tree.failureError()
	}
	return projectResult[O](h.process.result()), nil
}

// awaitCommitted serves a tree another writer owns: it waits on the committed
// log for this process's exit and then reads the result from the recovery cut.
func (h *Handle[O]) awaitCommitted(ctx context.Context) (Result[O], error) {
	state, err := h.committedState(ctx)
	if err != nil {
		return Result[O]{}, err
	}
	if process := state.processes[h.id]; process != nil && process.exited != nil {
		return projectResult[O](process.result()), nil
	}
	for entry, err := range h.Observe(ctx, state.head) {
		if err != nil {
			return Result[O]{}, err
		}
		if exited, ok := entry.Record.(Exited); ok && exited.Process == h.id {
			break
		}
	}
	if state, err = h.committedState(ctx); err != nil {
		return Result[O]{}, err
	}
	process := state.processes[h.id]
	if process == nil || process.exited == nil {
		return Result[O]{}, ErrProcessNotFound
	}
	return projectResult[O](process.result()), nil
}

// A terminal tree may acquire later writer epochs without another Exit. The
// first pull fixes its committed head as this observation's endpoint; further
// recovery cannot extend it. An active observation ends after the snapshot
// containing the root's exit. Local commits wake readers; other writers are
// polled, with each read starting after the last delivered position.
func (h *Handle[O]) Observe(ctx context.Context, after LSN) iter.Seq2[Entry, error] {
	return func(yield func(Entry, error) bool) {
		ctx = RequireContext(ctx)
		root := h.id.Root()
		through, err := h.terminalHead(ctx, after)
		if err != nil {
			yield(Entry{}, err)
			return
		}
		if through != 0 && after == through {
			return
		}
		cursor := after
		for {
			commit := h.nextCommit()
			entries, err := h.runtime.read(ctx, root, cursor)
			if err == nil && through != 0 && (len(entries) == 0 || entries[len(entries)-1].LSN < through) {
				err = ErrInvalidLog
			}
			if err != nil {
				yield(Entry{}, err)
				return
			}
			next, open := yieldObservations(ctx, root, entries, through, yield)
			if !open {
				return
			}
			cursor = max(cursor, next)
			if err := h.waitForCommit(ctx, commit); err != nil {
				yield(Entry{}, err)
				return
			}
		}
	}
}

func yieldObservations(ctx context.Context, root ProcessID, entries []Entry, through LSN, yield func(Entry, error) bool) (LSN, bool) {
	var last LSN
	exited := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			yield(Entry{}, err)
			return last, false
		}
		if !yield(entry, nil) {
			return last, false
		}
		last = entry.LSN
		if last == through {
			return last, false
		}
		if record, ok := entry.Record.(Exited); ok && record.Process == root {
			exited = true
		}
	}
	return last, !exited
}

func (h *Handle[O]) terminalHead(ctx context.Context, position LSN) (LSN, error) {
	state, err := h.committedState(ctx)
	if err != nil {
		return 0, err
	}
	if position > state.head {
		return 0, ErrCommitConflict
	}
	if state.processes[state.root].exited != nil {
		return state.head, nil
	}
	return 0, nil
}

func (h *Handle[O]) committedState(ctx context.Context) (*treeState, error) {
	return h.runtime.committed(ctx, h.id.Root())
}

// nextCommit is taken before a read so a commit landing between the read and
// the wait still wakes the reader.
func (h *Handle[O]) nextCommit() <-chan struct{} {
	if h.tree == nil {
		return nil
	}
	return h.tree.nextCommit()
}

func (h *Handle[O]) waitForCommit(ctx context.Context, commit <-chan struct{}) error {
	if commit == nil {
		timer := time.NewTimer(observationPollInterval)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case <-commit:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Handle[O]) request(ctx context.Context, process ProcessID, operation command) (commandResponse, error) {
	ctx = RequireContext(ctx)
	if err := ctx.Err(); err != nil {
		return commandResponse{}, err
	}
	if h.tree == nil {
		return commandResponse{}, ErrProcessNotActive
	}
	request := treeCommand{process: process, operation: operation, reply: make(chan commandResponse, 1), ctx: ctx}
	select {
	case h.tree.commands <- request:
	case <-h.tree.done:
		return commandResponse{}, h.tree.failureError()
	case <-ctx.Done():
		return commandResponse{}, ctx.Err()
	}
	select {
	case response := <-request.reply:
		return response, response.err
	case <-h.tree.done:
		select {
		case response := <-request.reply:
			return response, response.err
		default:
			return commandResponse{}, h.tree.failureError()
		}
	case <-ctx.Done():
		return commandResponse{}, ctx.Err()
	}
}

type Result[O any] struct {
	process     ProcessID
	startedAt   time.Time
	finishedAt  time.Time
	termination Termination
	usage       Usage
}

func projectResult[O any](r Result[Payload]) Result[O] {
	return Result[O](r)
}

func (r Result[O]) Output() (O, error) {
	var zero O
	if !r.Valid() {
		return zero, errors.New("run: no terminal result")
	}
	if output, ok := r.termination.Output(); ok {
		return output.Decode[O]()
	}
	return zero, &TerminationError{ID: r.process, Termination: r.termination}
}

func (r Result[O]) ProcessID() ProcessID     { return r.process }
func (r Result[O]) StartedAt() time.Time     { return r.startedAt }
func (r Result[O]) FinishedAt() time.Time    { return r.finishedAt }
func (r Result[O]) Termination() Termination { return r.termination }
func (r Result[O]) Usage() Usage             { return r.usage }
func (r Result[O]) Valid() bool {
	return r.process.Valid() && !r.startedAt.IsZero() && !r.finishedAt.IsZero() && r.termination.Valid()
}

type TerminationError struct {
	ID          ProcessID
	Termination Termination
}

func (t *TerminationError) Error() string {
	return fmt.Sprintf("run %s: %s", t.ID, t.Termination.Status())
}

// command is one Handle request applied by the tree owner. A recordingCommand
// answers with the records it commits; a replayCommand answers when the
// redispatched Effect settles.
type command interface{ command() }

type recordingCommand interface {
	command
	records(t *treeRuntime, p *processState) ([]Record, error)
}

type signalCommand struct{ signals []Signal }

func (signalCommand) command() {}

func (s signalCommand) records(t *treeRuntime, p *processState) ([]Record, error) {
	return t.signals(p, s.signals)
}

type controlCommand struct {
	kind   ControlKind
	reason string
}

func (controlCommand) command() {}

func (c controlCommand) records(_ *treeRuntime, p *processState) ([]Record, error) {
	control := ControlRequested{Origin: ControlOriginHost, Process: p.started.Process, Kind: c.kind, Reason: c.reason, At: canonicalTime(time.Now())}
	if !control.valid() || !p.acceptsControl(c.kind) {
		return nil, ErrInvalidProcessControl
	}
	return []Record{control}, nil
}

type resolveCommand struct {
	id      EffectID
	outcome Settlement
}

func (resolveCommand) command() {}

func (r resolveCommand) records(t *treeRuntime, p *processState) ([]Record, error) {
	if !p.unknownEffect(r.id) || !r.outcome.Valid() || r.outcome.Status() == SettlementStatusUnknown || t.jobs[r.id] != nil {
		return nil, ErrEffectNotPending
	}
	return []Record{EffectSettled{ID: r.id, Outcome: r.outcome, At: canonicalTime(time.Now())}}, nil
}

type replayCommand struct{ id EffectID }

func (replayCommand) command() {}

type treeCommand struct {
	process   ProcessID
	operation command
	reply     chan commandResponse
	ctx       context.Context
}

type commandResponse struct {
	accepted bool
	err      error
}
