package runtest

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/Tangerg/scope/agent/run"
)

var (
	ErrInvalidDispatchScript = errors.New("runtest: invalid dispatch script")
	ErrUnexpectedDispatch    = errors.New("runtest: unexpected dispatch")
	ErrEffectMismatch        = errors.New("runtest: effect does not match script")
)

// ScriptedCall describes one expected Dispatcher call and its deterministic
// stream and settlement outcome. ExpectedEffect is optional; when present, the
// complete immutable Effect must match. Error may follow emitted Deltas and is
// mutually exclusive with SettlementStatus and SettlementPayload.
type ScriptedCall struct {
	ExpectedEffect *run.Effect
	// Deltas are emitted in declaration order before the final outcome.
	Deltas            []json.RawMessage
	SettlementStatus  run.SettlementStatus
	SettlementPayload json.RawMessage
	Failure           run.Failure
	// Error makes Dispatch return an indeterminate external error.
	Error error
}

type ScriptedDispatcherConfig struct {
	ReplayPolicy run.ReplayPolicy
	// Calls are consumed in actual Dispatch order.
	Calls []ScriptedCall
}

type frozenCall struct {
	expectedEffectJSON []byte
	deltas             []json.RawMessage
	settlement         run.Settlement
	err                error
}

// ScriptedDispatcher is a concurrency-safe finite Dispatcher fixture. It
// records every request that reaches script consumption and fails calls made
// beyond or contrary to the configured script.
type ScriptedDispatcher struct {
	replayPolicy run.ReplayPolicy

	mu    sync.Mutex
	calls []frozenCall
	// requests records every consumed request; the next scripted call is the
	// one at its length.
	requests []run.EffectRequest
}

// NewScriptedDispatcher copies the script so callers cannot change expectations
// while dispatch is running.
func NewScriptedDispatcher(config ScriptedDispatcherConfig) (*ScriptedDispatcher, error) {
	if !config.ReplayPolicy.Valid() {
		return nil, fmt.Errorf("%w: ReplayPolicy is required", ErrInvalidDispatchScript)
	}
	calls := make([]frozenCall, len(config.Calls))
	for index, source := range config.Calls {
		call, err := freezeCall(source)
		if err != nil {
			return nil, fmt.Errorf("%w: Calls[%d]: %w", ErrInvalidDispatchScript, index, err)
		}
		calls[index] = call
	}
	return &ScriptedDispatcher{replayPolicy: config.ReplayPolicy, calls: calls}, nil
}

func freezeCall(source ScriptedCall) (frozenCall, error) {
	call := frozenCall{err: source.Error}
	if source.ExpectedEffect != nil {
		if !source.ExpectedEffect.Valid() {
			return frozenCall{}, run.ErrInvalidEffect
		}
		encoded, err := jsonv2.Marshal(*source.ExpectedEffect)
		if err != nil {
			return frozenCall{}, fmt.Errorf("encode expected Effect: %w", err)
		}
		call.expectedEffectJSON = encoded
	}
	call.deltas = make([]json.RawMessage, len(source.Deltas))
	for index, delta := range source.Deltas {
		if !jsontext.Value(delta).IsValid() {
			return frozenCall{}, fmt.Errorf("deltas[%d] is not valid JSON", index)
		}
		call.deltas[index] = bytes.Clone(delta)
	}
	if source.Error != nil {
		if source.SettlementStatus != run.SettlementStatusInvalid || len(source.SettlementPayload) != 0 || source.Failure.Valid() {
			return frozenCall{}, errors.New("error cannot be combined with a settlement")
		}
		return call, nil
	}
	var err error
	if source.SettlementStatus == run.SettlementStatusFailed {
		var details run.Payload
		if len(source.SettlementPayload) != 0 {
			details, err = run.ParsePayload(source.SettlementPayload)
			if err != nil {
				return frozenCall{}, err
			}
		}
		call.settlement, err = run.NewFailedSettlement(source.Failure, details)
	} else if source.Failure.Valid() {
		return frozenCall{}, errors.New("only a failed settlement carries a Failure")
	} else {
		call.settlement, err = run.NewSettlement(source.SettlementStatus, source.SettlementPayload)
	}
	if err != nil {
		return frozenCall{}, err
	}
	return call, nil
}

func (s *ScriptedDispatcher) Dispatch(
	ctx context.Context,
	request run.EffectRequest,
	emit run.DeltaEmitter,
) (run.Settlement, error) {
	if err := ctx.Err(); err != nil {
		return run.Settlement{}, err
	}
	if s == nil {
		return run.Settlement{}, ErrInvalidDispatchScript
	}
	call, err := s.consume(request)
	if err != nil {
		return run.Settlement{}, err
	}
	return call.dispatch(request, emit)
}

func (s *ScriptedDispatcher) consume(request run.EffectRequest) (frozenCall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := len(s.requests)
	s.requests = append(s.requests, request)
	if next >= len(s.calls) {
		return frozenCall{}, ErrUnexpectedDispatch
	}
	return s.calls[next], nil
}

func (f frozenCall) dispatch(
	request run.EffectRequest,
	emit run.DeltaEmitter,
) (run.Settlement, error) {
	matches, err := f.matches(request.Effect())
	if err != nil {
		return run.Settlement{}, fmt.Errorf("%w: %w", ErrEffectMismatch, err)
	}
	if !matches {
		return run.Settlement{}, ErrEffectMismatch
	}
	if emit != nil {
		for _, delta := range f.deltas {
			emit(bytes.Clone(delta))
		}
	}
	if f.err != nil {
		return run.Settlement{}, f.err
	}
	return f.settlement, nil
}

func (f frozenCall) matches(effect run.Effect) (bool, error) {
	if f.expectedEffectJSON == nil {
		return true, nil
	}
	actual, err := jsonv2.Marshal(effect)
	if err != nil {
		return false, err
	}
	return bytes.Equal(f.expectedEffectJSON, actual), nil
}

func (s *ScriptedDispatcher) Policy(effect run.Effect) run.EffectPolicy {
	if s == nil || !effect.Valid() {
		return run.EffectPolicy{Replay: run.ReplayPolicyInvalid}
	}
	return run.EffectPolicy{Replay: s.replayPolicy}
}

// Requests returns consumed Dispatch requests in actual call order, including
// requests that mismatch or exceed the script.
func (s *ScriptedDispatcher) Requests() []run.EffectRequest {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

func (s *ScriptedDispatcher) Remaining() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return max(len(s.calls)-len(s.requests), 0)
}
