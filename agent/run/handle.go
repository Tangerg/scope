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
	log, found, err := h.runtime.load(ctx, h.id.Root())
	if err != nil {
		return err
	}
	if !found {
		return ErrTreeNotFound
	}
	if validationErr := log.Validate(); validationErr != nil {
		return validationErr
	}
	receipts := make(map[SignalID]SignalReceipt)
	exists, terminal := false, false
	for _, entry := range log.Entries {
		switch value := entry.Record.(type) {
		case Started:
			if value.Process == h.id {
				exists = true
			}
		case Exited:
			if value.Process == h.id {
				terminal = true
			}
		case SignalAdmitted:
			if value.Process == h.id {
				receipts[value.Signal.ID] = SignalReceipt{ID: value.Signal.ID, Wait: value.Signal.Wait, Digest: value.Signal.digest()}
			}
		}
	}
	if !exists {
		return ErrProcessNotFound
	}
	fresh, err := signalBatch(signals).unadmitted(receipts)
	if err != nil {
		return err
	}
	if len(fresh) == 0 {
		return nil
	}
	if terminal {
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
		select {
		case <-h.process.done:
			h.tree.mu.RLock()
			result := h.process.result()
			h.tree.mu.RUnlock()
			return projectResult[O](result), nil
		case <-h.tree.done:
			h.tree.mu.RLock()
			if h.process.exited != nil {
				result := projectResult[O](h.process.result())
				h.tree.mu.RUnlock()
				return result, nil
			}
			h.tree.mu.RUnlock()
			return Result[O]{}, h.tree.failureError()
		case <-ctx.Done():
			return Result[O]{}, ctx.Err()
		}
	}
	for observation, err := range h.observations(ctx) {
		if err != nil {
			return Result[O]{}, err
		}
		p := observation.state.processes[h.id]
		if p == nil {
			return Result[O]{}, ErrProcessNotFound
		}
		if p.exited != nil {
			return projectResult[O](p.result()), nil
		}
	}
	return Result[O]{}, ErrProcessNotFound
}
func (h *Handle[O]) Join(ctx context.Context) error { _, err := h.Await(ctx); return err }
func (h *Handle[O]) Observe(ctx context.Context, after LSN) iter.Seq2[Entry, error] {
	return func(yield func(Entry, error) bool) {
		cursor := after
		for observation, err := range h.observations(ctx) {
			if err != nil {
				yield(Entry{}, err)
				return
			}
			if cursor > observation.state.head {
				yield(Entry{}, ErrCommitConflict)
				return
			}
			for _, entry := range observation.log.Entries {
				if entry.LSN <= cursor {
					continue
				}
				if !yield(entry, nil) {
					return
				}
				cursor = entry.LSN
			}
		}
	}
}
func (h *Handle[O]) observations(ctx context.Context) iter.Seq2[treeObservation, error] {
	return func(yield func(treeObservation, error) bool) {
		ctx = RequireContext(ctx)
		ticker := time.NewTicker(observationPollInterval)
		defer ticker.Stop()
		for {
			log, found, err := h.runtime.load(ctx, h.id.Root())
			if err != nil {
				yield(treeObservation{}, err)
				return
			}
			if !found {
				yield(treeObservation{}, ErrTreeNotFound)
				return
			}
			state, err := projectLog(log)
			if err != nil {
				yield(treeObservation{}, err)
				return
			}
			if !yield(treeObservation{log: log, state: state}, nil) {
				return
			}
			if state.processes[state.root].exited != nil {
				return
			}
			select {
			case <-ctx.Done():
				yield(treeObservation{}, ctx.Err())
				return
			case <-ticker.C:
			}
		}
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

type treeObservation struct {
	log   Log
	state *treeState
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

type command interface{ command() }
type signalCommand struct{ signals []Signal }

func (signalCommand) command() {}

type controlCommand struct {
	kind   ControlKind
	reason string
}

func (controlCommand) command() {}

type resolveCommand struct {
	id      EffectID
	outcome Settlement
}

func (resolveCommand) command() {}

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
