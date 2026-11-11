package run

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

var (
	ErrInvalidConfig = errors.New("run: invalid runtime configuration")
	ErrClosed        = errors.New("run: runtime is closed")
	// ErrWriterStopped is the work context's cancellation cause when its local
	// writer ends. It does not declare cancellation of the durable Process.
	ErrWriterStopped         = errors.New("run: local writer stopped")
	ErrProcessNotFound       = errors.New("run: process not found")
	ErrProcessNotActive      = errors.New("run: process has no active local writer")
	ErrProcessFinished       = errors.New("run: process has finished")
	ErrActiveProcesses       = errors.New("run: runtime has active processes")
	ErrTreeNotFound          = errors.New("run: tree not found")
	ErrEffectNotPending      = errors.New("run: effect does not require resolution")
	ErrEffectReplayForbidden = errors.New("run: effect cannot be replayed under the same identity")
	ErrEffectOutcomeUnknown  = errors.New("run: effect outcome remains unknown")
	ErrInvalidProcessControl = errors.New("run: invalid process control")
	ErrNilContext            = errors.New("run: nil context")
)

func requireContext(ctx context.Context) context.Context {
	if ctx == nil {
		panic(ErrNilContext)
	}
	return ctx
}

type RuntimeError struct {
	process    ProcessID
	epoch      Epoch
	after      LSN
	unresolved []EffectID
	cause      error
}

func (r *RuntimeError) Error() string {
	return fmt.Sprintf("run: runtime for %s stopped at %d: %v", r.process, r.after, r.cause)
}

func (r *RuntimeError) Unwrap() error                   { return r.cause }
func (r *RuntimeError) ProcessID() ProcessID            { return r.process }
func (r *RuntimeError) Epoch() Epoch                    { return r.epoch }
func (r *RuntimeError) After() LSN                      { return r.after }
func (r *RuntimeError) UnresolvedEffectIDs() []EffectID { return slices.Clone(r.unresolved) }

func canonicalEffectIDs(effectIDs []EffectID) []EffectID {
	canonical := slices.Clone(effectIDs)
	slices.SortFunc(canonical, EffectID.compare)
	return slices.Compact(canonical)
}

func dispatchFailure(err error) Failure {
	kind := failureKindForError(err, FailureKindExternal)
	code := failureCodeRuntimeDispatchFailed
	if kind == FailureKindPanic {
		code = failureCodeRuntimeDispatchPanicked
	}
	return newRuntimeFailure(kind, code, err)
}
