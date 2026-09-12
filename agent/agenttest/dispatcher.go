package agenttest

import (
	"bytes"
	"context"
	"encoding/json"
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

// DispatchStep describes one expected Dispatcher call and its deterministic
// stream and settlement outcome. ExpectedEffect is optional; when present, the
// complete immutable Effect must match. Error may follow emitted Deltas and is
// mutually exclusive with SettlementStatus and SettlementPayload.
type DispatchStep struct {
	// ExpectedEffect is the optional complete Effect expected at this step.
	ExpectedEffect *agent.Effect
	// Deltas are emitted in declaration order before the final outcome.
	Deltas []json.RawMessage
	// SettlementStatus is the definite or unknown settlement status.
	SettlementStatus agent.SettlementStatus
	// SettlementPayload is the Strategy-owned settlement payload.
	SettlementPayload json.RawMessage
	// Error makes Dispatch return an indeterminate external error.
	Error error
}

// ScriptedDispatcherConfig declares a finite deterministic dispatch script.
type ScriptedDispatcherConfig struct {
	// ReplayPolicy is returned for every valid Dispatcher Effect.
	ReplayPolicy agent.ReplayPolicy
	// Steps are consumed in actual Dispatch order.
	Steps []DispatchStep
}

type dispatchStep struct {
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
	steps    []dispatchStep
	next     int
	requests []agent.EffectRequest
}

// NewScriptedDispatcher freezes the script at construction so a test cannot
// mutate expectations while the Engine is running against them. It fails calls
// that go beyond or contrary to the script rather than returning a zero
// settlement, because a dispatcher that quietly answers anything turns an
// ordering bug into a passing test.
func NewScriptedDispatcher(config ScriptedDispatcherConfig) (*ScriptedDispatcher, error) {
	if config.ReplayPolicy != agent.ReplayPolicyNever &&
		config.ReplayPolicy != agent.ReplayPolicySameIdentity {
		return nil, fmt.Errorf("%w: ReplayPolicy is required", ErrInvalidDispatchScript)
	}
	steps := make([]dispatchStep, len(config.Steps))
	for index, source := range config.Steps {
		step, err := freezeDispatchStep(source)
		if err != nil {
			return nil, fmt.Errorf("%w: Steps[%d]: %w", ErrInvalidDispatchScript, index, err)
		}
		steps[index] = step
	}
	return &ScriptedDispatcher{replayPolicy: config.ReplayPolicy, steps: steps}, nil
}

func freezeDispatchStep(source DispatchStep) (dispatchStep, error) {
	step := dispatchStep{settlementStatus: source.SettlementStatus, err: source.Error}
	if source.ExpectedEffect != nil {
		if !source.ExpectedEffect.Valid() {
			return dispatchStep{}, agent.ErrInvalidEffect
		}
		encoded, err := json.Marshal(*source.ExpectedEffect)
		if err != nil {
			return dispatchStep{}, fmt.Errorf("encode expected Effect: %w", err)
		}
		step.expectedEffectJSON = encoded
	}
	step.deltas = make([]json.RawMessage, len(source.Deltas))
	for index, delta := range source.Deltas {
		if !json.Valid(delta) {
			return dispatchStep{}, fmt.Errorf("deltas[%d] is not valid JSON", index)
		}
		step.deltas[index] = bytes.Clone(delta)
	}
	if source.Error != nil {
		if source.SettlementStatus != agent.SettlementStatusInvalid || len(source.SettlementPayload) != 0 {
			return dispatchStep{}, errors.New("error cannot be combined with a settlement")
		}
		return step, nil
	}
	if source.SettlementStatus < agent.SettlementStatusSucceeded ||
		source.SettlementStatus > agent.SettlementStatusUnknown {
		return dispatchStep{}, errors.New("settlement status is required")
	}
	if !json.Valid(source.SettlementPayload) {
		return dispatchStep{}, errors.New("settlement payload is not valid JSON")
	}
	step.settlementPayload = bytes.Clone(source.SettlementPayload)
	return step, nil
}

// Dispatch consumes the next scripted step, emits its Deltas, and returns its
// configured settlement or error.
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
	step, err := s.consume(request)
	if err != nil {
		return agent.Settlement{}, err
	}
	return step.dispatch(request, emit)
}

func (s *ScriptedDispatcher) consume(request agent.EffectRequest) (dispatchStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	if s.next >= len(s.steps) {
		return dispatchStep{}, ErrUnexpectedDispatch
	}
	step := s.steps[s.next]
	s.next++
	return step, nil
}

func (d dispatchStep) dispatch(
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

func (d dispatchStep) matches(effect agent.Effect) (bool, error) {
	if d.expectedEffectJSON == nil {
		return true, nil
	}
	actual, err := json.Marshal(effect)
	if err != nil {
		return false, err
	}
	return bytes.Equal(d.expectedEffectJSON, actual), nil
}

// ReplayPolicy returns the immutable policy declared at construction.
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

// Remaining reports how many scripted calls have not been consumed.
func (s *ScriptedDispatcher) Remaining() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.steps) - s.next
}
