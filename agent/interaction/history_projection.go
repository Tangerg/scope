package interaction

import (
	"fmt"
	"sync"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

// HistoryProjection reads one interaction Program's settled tool rounds and
// delegate attributions from the complete history that run.Runtime.History
// returns. A checkpoint does not keep settled rounds, so every read validates
// and folds the whole caller-owned Log, exactly as the first read did.
//
// Strictly decoding committed model requests dominates that fold and grows with
// the conversation, so the projection remembers what each dispatcher Effect of
// its latest successfully projected history decoded to, keyed by EffectID and
// its exact canonical payload. A host re-reading a growing history after each
// commit therefore decodes every committed request once. The projection retains
// those payloads until its next successful read. It is safe for concurrent use
// and owns no acknowledgment, execution, persistence or retry policy.
type HistoryProjection struct {
	program    run.ProgramRef
	definition *definition
	mu         sync.Mutex
	dispatches map[run.EffectID]decodedDispatch
}

// NewHistoryProjection binds a projection to a compiled interaction Program.
// Programs with the same Go types but another definition are rejected.
func NewHistoryProjection[I, O any](program run.Program[I, O]) (*HistoryProjection, error) {
	if !program.Valid() {
		return nil, ErrInvalidResult
	}
	definition, bound := program.Definition().(*definition)
	if !bound {
		return nil, ErrInvalidResult
	}
	return &HistoryProjection{program: program.ProgramRef(), definition: definition}, nil
}

// SettledResults returns the rounds with at least one settled call. Only
// committed intents establish references. Locally rejected proposals that never
// emitted an effect are not presented as externally executed operations.
func (h *HistoryProjection) SettledResults(log run.Log) ([]RoundResults, error) {
	projection, err := h.project(log)
	if err != nil {
		return nil, err
	}
	var result []RoundResults
	for _, round := range projection.rounds {
		if len(round.entries) == 0 {
			continue
		}
		if err := round.seal(); err != nil {
			return nil, err
		}
		result = append(result, *round)
	}
	return result, nil
}

// DelegateAttributions returns the started delegate children and the proposed
// calls that own them.
func (h *HistoryProjection) DelegateAttributions(log run.Log) ([]DelegateAttribution, error) {
	projection, err := h.project(log)
	if err != nil {
		return nil, err
	}
	return projection.delegates, nil
}

func (h *HistoryProjection) project(log run.Log) (interactionProjection, error) {
	if h == nil || h.definition == nil {
		return interactionProjection{}, ErrInvalidResult
	}
	if !log.Complete() {
		return interactionProjection{}, fmt.Errorf("%w: projection requires the complete history", ErrInvalidResult)
	}
	if err := log.Validate(); err != nil {
		return interactionProjection{}, err
	}
	h.mu.Lock()
	decoded := h.dispatches
	h.mu.Unlock()
	projector := newHistoryProjector(h.program, h.definition, decoded)
	for _, entry := range log.Entries {
		if err := projector.apply(entry.Record); err != nil {
			return interactionProjection{}, err
		}
	}
	// Published maps are never mutated, so a concurrent read may still use the
	// previous one; either is a correct cache of the same pure decoding.
	h.mu.Lock()
	h.dispatches = projector.dispatches
	h.mu.Unlock()
	return projector.projection, nil
}

// decodedDispatch is what later records need from one strictly decoded dispatcher
// Effect. Decoding depends only on the canonical payload, so the same Effect
// with identical payload bytes reuses its decoding instead of decoding again.
type decodedDispatch struct {
	payload           run.Payload
	operation         operation
	modelCallSequence uint64
	toolCall          toolDispatchRequest
}

func decodeDispatch(payload run.Payload) (decodedDispatch, error) {
	envelope, err := decodeEffect(payload)
	if err != nil {
		return decodedDispatch{}, err
	}
	dispatch := decodedDispatch{payload: payload, operation: envelope.operation()}
	switch dispatch.operation {
	case operationModelCall:
		dispatch.modelCallSequence = envelope.ModelCall.ModelCallSequence
	case operationToolCall:
		dispatch.toolCall = *envelope.ToolCall
	}
	return dispatch, nil
}

type interactionProjection struct {
	rounds    []*RoundResults
	delegates []DelegateAttribution
}

// historyProjector folds one Program's committed history into its tool rounds
// and delegate attributions. Each method owns one record kind, so the rules
// pairing a settlement with its intent have a single home.
type historyProjector struct {
	program    run.ProgramRef
	definition *definition
	projection interactionProjection
	// members marks the processes that run this Program; others are skipped.
	members map[run.ProcessID]bool
	intents map[run.EffectID]run.EffectIntent
	// decoded is the previous read's cache; dispatches holds only this history's facts.
	decoded    map[run.EffectID]decodedDispatch
	dispatches map[run.EffectID]decodedDispatch
	models     map[run.EffectID]uint64
	rounds     map[run.ProcessID]*RoundResults
	calls      map[run.EffectID]*projectedCall
	delegates  map[run.ProcessID]*projectedCall
}

func newHistoryProjector(program run.ProgramRef, definition *definition, decoded map[run.EffectID]decodedDispatch) *historyProjector {
	return &historyProjector{
		program:    program,
		definition: definition,
		members:    make(map[run.ProcessID]bool),
		intents:    make(map[run.EffectID]run.EffectIntent),
		decoded:    decoded,
		dispatches: make(map[run.EffectID]decodedDispatch, len(decoded)),
		models:     make(map[run.EffectID]uint64),
		rounds:     make(map[run.ProcessID]*RoundResults),
		calls:      make(map[run.EffectID]*projectedCall),
		delegates:  make(map[run.ProcessID]*projectedCall),
	}
}

func (h *historyProjector) apply(record run.Record) error {
	switch record := record.(type) {
	case run.Started:
		h.members[record.Process] = record.Program == h.program
		return nil
	case run.EffectIntent:
		return h.intended(record)
	case run.EffectSettled:
		return h.settled(record)
	case run.Exited:
		return h.exited(record)
	default:
		return nil
	}
}

func (h *historyProjector) intended(intent run.EffectIntent) error {
	if !h.members[intent.ID.Process] {
		return nil
	}
	h.intents[intent.ID] = intent
	switch intent.Effect.Target() {
	case run.EffectTargetDispatcher:
		return h.intendedDispatch(intent)
	case run.EffectTargetFramework:
		return h.intendedDelegate(intent)
	default:
		return nil
	}
}

func (h *historyProjector) intendedDispatch(intent run.EffectIntent) error {
	payload := intent.Effect.Payload()
	dispatch, known := h.decoded[intent.ID]
	if !known || !dispatch.payload.Equal(payload) {
		var err error
		if dispatch, err = decodeDispatch(payload); err != nil {
			return err
		}
	}
	h.dispatches[intent.ID] = dispatch
	switch dispatch.operation {
	case operationModelCall:
		h.models[intent.ID] = dispatch.modelCallSequence
		return nil
	case operationToolCall:
		return h.intendedToolCall(intent.ID, dispatch.toolCall)
	default:
		return nil
	}
}

func (h *historyProjector) intendedToolCall(effect run.EffectID, request toolDispatchRequest) error {
	if request.Resume != nil {
		previous, found := h.calls[request.Resume.Reference]
		if !found {
			return ErrInvalidResult
		}
		if err := previous.resume(request, effect); err != nil {
			return err
		}
		h.calls[effect] = previous
		return nil
	}
	round := h.rounds[effect.Process]
	index, found := round.callIndex(request.Invocation.Call)
	if !found {
		return ErrInvalidResult
	}
	return h.claim(round, index, effect)
}

func (h *historyProjector) intendedDelegate(intent run.EffectIntent) error {
	spec, ok := intent.Effect.ChildStart()
	if !ok {
		return run.ErrInvalidChildStart
	}
	round := h.rounds[intent.ID.Process]
	if round == nil {
		return ErrInvalidResult
	}
	index, err := round.delegateIndex(h.definition, spec)
	if err != nil {
		return err
	}
	return h.claim(round, index, intent.ID)
}

func (h *historyProjector) claim(round *RoundResults, index uint32, effect run.EffectID) error {
	if err := round.claim(index, effect); err != nil {
		return err
	}
	h.calls[effect] = &projectedCall{round: round, index: index}
	return nil
}

// A settlement carrying a runtime Failure records no outcome of the operation
// itself, so it neither opens a round nor completes a call.
func (h *historyProjector) settled(settled run.EffectSettled) error {
	if !h.members[settled.ID.Process] || settled.Failure != nil {
		return nil
	}
	intent, found := h.intents[settled.ID]
	if !found {
		return ErrInvalidResult
	}
	if sequence, modelCall := h.models[settled.ID]; modelCall {
		return h.settledModel(settled, sequence)
	}
	call, found := h.calls[settled.ID]
	if !found {
		return nil
	}
	if intent.Effect.Target() == run.EffectTargetFramework {
		return h.settledDelegateStart(settled, call)
	}
	return h.settledToolCall(settled, call)
}

func (h *historyProjector) settledModel(settled run.EffectSettled, sequence uint64) error {
	if settled.Outcome.Status() != run.SettlementStatusSucceeded {
		return nil
	}
	envelope, err := decodeResult(settled.Outcome.Payload())
	if err != nil {
		return err
	}
	if envelope.ModelResult == nil {
		return ErrInvalidResult
	}
	round := &RoundResults{model: settled.ID, sequence: sequence, calls: proposedCalls(envelope.ModelResult.Response), references: make(map[uint32]run.EffectID)}
	h.rounds[settled.ID.Process] = round
	h.projection.rounds = append(h.projection.rounds, round)
	return nil
}

func (h *historyProjector) settledDelegateStart(settled run.EffectSettled, call *projectedCall) error {
	child, rejection, err := settled.Outcome.SpawnResult()
	if err != nil {
		return err
	}
	if !child.Valid() {
		return call.round.result(call.index, rejectedDelegateStartResult(rejection))
	}
	h.delegates[child] = call
	h.projection.delegates = append(h.projection.delegates, DelegateAttribution{reference: settled.ID, modelCallSequence: call.round.sequence, toolCallIndex: call.index, toolCall: call.toolCall(), processID: child})
	return nil
}

func (h *historyProjector) settledToolCall(settled run.EffectSettled, call *projectedCall) error {
	if settled.Outcome.Status() != run.SettlementStatusSucceeded {
		return nil
	}
	envelope, err := decodeResult(settled.Outcome.Payload())
	if err != nil {
		return err
	}
	if envelope.ToolResult == nil {
		return ErrInvalidResult
	}
	if request := envelope.ToolResult.InputRequest; request != nil {
		call.input = &inputWait{Effect: settled.ID, Request: *request}
		return nil
	}
	return call.round.result(call.index, *envelope.ToolResult.Completion)
}

func (h *historyProjector) exited(exited run.Exited) error {
	call, found := h.delegates[exited.Process]
	if !found {
		return nil
	}
	if output, completed := exited.Termination.Output(); completed {
		delegate, _ := h.definition.delegate(call.toolCall().Name)
		if err := delegate.validateOutput(output); err != nil {
			return fmt.Errorf("%w: delegated output: %w", ErrInvalidResult, err)
		}
	}
	result, err := delegateCallResult(exited.Termination)
	if err != nil {
		return err
	}
	return call.round.result(call.index, result)
}

// projectedCall is one proposed call and the question it is waiting on, if any;
// every continuation Effect of the call resolves to the same value.
type projectedCall struct {
	round *RoundResults
	index uint32
	input *inputWait
}

func (p *projectedCall) toolCall() chat.ToolCall { return p.round.calls[p.index] }

func (p *projectedCall) resume(request toolDispatchRequest, effect run.EffectID) error {
	resume := request.Resume
	if resume == nil || !resume.precedes(effect) || p.input == nil || p.round.references[p.index] != resume.Reference || p.toolCall() != request.Invocation.Call {
		return ErrInvalidResult
	}
	if !p.input.Request.Equal(resume.InputRequest) {
		return ErrInvalidResult
	}
	p.input = nil
	return nil
}
