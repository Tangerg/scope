package agenttest

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

	agent "github.com/Tangerg/scope/agent"
)

var (
	ErrInvalidDispatchScript = errors.New("agenttest: invalid dispatch script")
	ErrUnexpectedDispatch    = errors.New("agenttest: unexpected dispatch")
	ErrEffectMismatch        = errors.New("agenttest: effect does not match script")
)

// ScriptedCall describes one expected Dispatcher call and its deterministic
// stream and settlement outcome. ExpectedEffect is optional; when present, the
// complete immutable Effect must match. Error may follow emitted Deltas and is
// mutually exclusive with SettlementStatus and SettlementPayload.
type ScriptedCall struct {
	ExpectedEffect *agent.Effect
	// Deltas are emitted in declaration order before the final outcome.
	Deltas            []json.RawMessage
	SettlementStatus  agent.SettlementStatus
	SettlementPayload json.RawMessage
	// Error makes Dispatch return an indeterminate external error.
	Error error
}

type ScriptedDispatcherConfig struct {
	ReplayPolicy agent.ReplayPolicy
	// Calls are consumed in actual Dispatch order.
	Calls []ScriptedCall
}

type frozenCall struct {
	expectedEffectJSON []byte
	deltas             []json.RawMessage
	settlementStatus   agent.SettlementStatus
	settlementPayload  json.RawMessage
	err                error
}

// ScriptedDispatcher is a concurrency-safe finite Dispatcher fixture. It
// records every request that reaches script consumption and fails calls made
// beyond or contrary to the configured script.
type ScriptedDispatcher struct {
	replayPolicy agent.ReplayPolicy

	mu       sync.Mutex
	calls    []frozenCall
	next     int
	requests []agent.EffectRequest
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
	call := frozenCall{settlementStatus: source.SettlementStatus, err: source.Error}
	if source.ExpectedEffect != nil {
		if !source.ExpectedEffect.Valid() {
			return frozenCall{}, agent.ErrInvalidEffect
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
		if source.SettlementStatus != agent.SettlementStatusInvalid || len(source.SettlementPayload) != 0 {
			return frozenCall{}, errors.New("error cannot be combined with a settlement")
		}
		return call, nil
	}
	if !source.SettlementStatus.Valid() {
		return frozenCall{}, errors.New("settlement status is required")
	}
	if !jsontext.Value(source.SettlementPayload).IsValid() {
		return frozenCall{}, errors.New("settlement payload is not valid JSON")
	}
	call.settlementPayload = bytes.Clone(source.SettlementPayload)
	return call, nil
}

func (s *ScriptedDispatcher) Dispatch(
	ctx context.Context,
	request agent.EffectRequest,
	emit agent.DeltaEmitter,
) (agent.Settlement, error) {
	if err := ctx.Err(); err != nil {
		return agent.Settlement{}, err
	}
	if s == nil {
		return agent.Settlement{}, ErrInvalidDispatchScript
	}
	call, err := s.consume(request)
	if err != nil {
		return agent.Settlement{}, err
	}
	return call.dispatch(request, emit)
}

func (s *ScriptedDispatcher) consume(request agent.EffectRequest) (frozenCall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	if s.next >= len(s.calls) {
		return frozenCall{}, ErrUnexpectedDispatch
	}
	call := s.calls[s.next]
	s.next++
	return call, nil
}

func (d frozenCall) dispatch(
	request agent.EffectRequest,
	emit agent.DeltaEmitter,
) (agent.Settlement, error) {
	matches, err := d.matches(request.Effect())
	if err != nil {
		return agent.Settlement{}, fmt.Errorf("%w: %w", ErrEffectMismatch, err)
	}
	if !matches {
		return agent.Settlement{}, ErrEffectMismatch
	}
	if emit != nil {
		for _, delta := range d.deltas {
			emit(bytes.Clone(delta))
		}
	}
	if d.err != nil {
		return agent.Settlement{}, d.err
	}
	return agent.NewSettlement(request.ID(), d.settlementStatus, d.settlementPayload)
}

func (d frozenCall) matches(effect agent.Effect) (bool, error) {
	if d.expectedEffectJSON == nil {
		return true, nil
	}
	actual, err := jsonv2.Marshal(effect)
	if err != nil {
		return false, err
	}
	return bytes.Equal(d.expectedEffectJSON, actual), nil
}

func (s *ScriptedDispatcher) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	if s == nil || !effect.Valid() {
		return agent.ReplayPolicyInvalid
	}
	return s.replayPolicy
}

// Requests returns consumed Dispatch requests in actual call order, including
// requests that mismatch or exceed the script.
func (s *ScriptedDispatcher) Requests() []agent.EffectRequest {
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
	return len(s.calls) - s.next
}
