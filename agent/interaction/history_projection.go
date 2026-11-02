package interaction

import (
	"bytes"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

type interactionProjection struct {
	rounds    []*RoundResults
	delegates []DelegateAttribution
}

func projectInteractions(program run.Program[Input, Output], log run.Log) (interactionProjection, error) {
	projector, err := newHistoryProjector(program)
	if err != nil {
		return interactionProjection{}, err
	}
	if !log.Complete() {
		return interactionProjection{}, fmt.Errorf("%w: projection requires the complete history", ErrInvalidResult)
	}
	if err := log.Validate(); err != nil {
		return interactionProjection{}, err
	}
	for _, entry := range log.Entries {
		if err := projector.apply(entry.Record); err != nil {
			return interactionProjection{}, err
		}
	}
	return projector.projection, nil
}

// historyProjector folds one Program's committed history into its tool rounds
// and delegate attributions. Each method owns one record kind, so the rules
// pairing a settlement with its intent have a single home.
type historyProjector struct {
	program    run.ProgramRef
	definition *definition
	projection interactionProjection
	// members marks the processes that run this Program; others are skipped.
	members   map[run.ProcessID]bool
	intents   map[run.EffectID]run.EffectIntent
	models    map[run.EffectID]modelCall
	rounds    map[run.ProcessID]*RoundResults
	calls     map[run.EffectID]*projectedCall
	delegates map[run.ProcessID]*projectedCall
}

func newHistoryProjector(program run.Program[Input, Output]) (*historyProjector, error) {
	if !program.Valid() {
		return nil, ErrInvalidResult
	}
	definition, bound := program.Definition().(*definition)
	if !bound {
		return nil, ErrInvalidResult
	}
	return &historyProjector{
		program:    program.ProgramRef(),
		definition: definition,
		members:    make(map[run.ProcessID]bool),
		intents:    make(map[run.EffectID]run.EffectIntent),
		models:     make(map[run.EffectID]modelCall),
		rounds:     make(map[run.ProcessID]*RoundResults),
		calls:      make(map[run.EffectID]*projectedCall),
		delegates:  make(map[run.ProcessID]*projectedCall),
	}, nil
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
	envelope, err := decodeEffect(intent.Effect.Payload().JSON())
	if err != nil {
		return err
	}
	switch envelope.operation() {
	case operationModelCall:
		h.models[intent.ID] = *envelope.ModelCall
		return nil
	case operationToolCall:
		return h.intendedToolCall(intent.ID, *envelope.ToolCall)
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
	spec, err := run.ParseChildStartEffect(intent.Effect)
	if err != nil {
		return err
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
	if model, modelCall := h.models[settled.ID]; modelCall {
		return h.settledModel(settled, model)
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

func (h *historyProjector) settledModel(settled run.EffectSettled, model modelCall) error {
	if settled.Outcome.Status() != run.SettlementStatusSucceeded {
		return nil
	}
	envelope, err := decodeResult(settled.Outcome.Payload().JSON())
	if err != nil {
		return err
	}
	if envelope.ModelResult == nil {
		return ErrInvalidResult
	}
	proposed, err := validatedToolCalls(envelope.ModelResult.Response)
	if err != nil {
		return err
	}
	round := &RoundResults{model: settled.ID, sequence: model.ModelCallSequence, calls: proposed, references: make(map[uint32]run.EffectID)}
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
		return call.round.result(call.index, *rejectedDelegateStartResult(call.toolCall(), rejection))
	}
	h.delegates[child] = call
	h.projection.delegates = append(h.projection.delegates, DelegateAttribution{reference: settled.ID, modelCallSequence: call.round.sequence, toolCallIndex: call.index, toolCall: call.toolCall(), processID: child})
	return nil
}

func (h *historyProjector) settledToolCall(settled run.EffectSettled, call *projectedCall) error {
	if settled.Outcome.Status() != run.SettlementStatusSucceeded {
		return nil
	}
	envelope, err := decodeResult(settled.Outcome.Payload().JSON())
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
	result, err := delegateToolResult(call.toolCall(), exited.Termination)
	if err != nil {
		return err
	}
	return call.round.result(call.index, newToolCallResult(result))
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
	question := p.input.Request
	if !question.ResponseSchema().Equal(resume.InputRequest.ResponseSchema()) || !bytes.Equal(question.Prompt(), resume.InputRequest.Prompt()) || !bytes.Equal(question.State(), resume.InputRequest.State()) {
		return ErrInvalidResult
	}
	p.input = nil
	return nil
}
