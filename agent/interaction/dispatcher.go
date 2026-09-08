package interaction

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/samber/lo"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// ModelClient stays consumer-owned so Interaction does not depend on one
// concrete Core client implementation.
type ModelClient interface {
	// Call invokes the configured model for one complete response.
	Call(ctx context.Context, request *chat.Request) (*chat.Response, error)
}

// ModelResponseMode selects the single model response lifecycle used by a
// Dispatcher. Streaming is observational; both modes settle the same complete
// provider-neutral Response.
type ModelResponseMode string

// Complete mode receives one response; stream mode accumulates response events
// before settling the same complete response contract.
const (
	ModelResponseComplete ModelResponseMode = ""
	ModelResponseStream   ModelResponseMode = "stream"
)

func (m ModelResponseMode) Valid() bool {
	return m == ModelResponseComplete || m == ModelResponseStream
}

// DispatcherConfig binds external capabilities for one Deployment.
type DispatcherConfig struct {
	// Client is the single model dependency. Stream mode requires the same value
	// to implement chat.Streamer so call and stream settings cannot diverge.
	Client ModelClient
	// ResponseMode selects complete or streaming model responses.
	ResponseMode ModelResponseMode

	// Tools is the frozen ordinary model-visible and executable Tool manifest.
	// Managed Delegate definitions come from the bound Definition.
	Tools []tool.Tool

	// DeferredTools is the frozen ordinary executable Tool set omitted from the
	// initial model manifest. An executing Tool may make exact names visible on
	// later model calls through AdvertiseTools; authority never changes.
	DeferredTools []tool.Tool

	// MaxConcurrentToolCalls bounds calls that explicitly declare safe overlap.
	// Zero preserves serial execution; negative values are invalid. Undeclared
	// calls and calls with the same non-empty concurrency key remain serial.
	MaxConcurrentToolCalls int

	// Observer receives exact model and Tool call facts. It is intentionally
	// separate from Engine Events/Deltas: those describe execution mechanics,
	// while this boundary exposes typed model and Tool semantics.
	Observer ExecutionObserver

	// ModelContextReducer optionally replaces only the provider-neutral message
	// context at the last safe boundary before each model call. The Dispatcher
	// installs the effective messages back into Interaction recovery state when
	// the call settles, so later calls and checkpoints cannot regrow a reduced
	// context from the pre-reduction Effect payload.
	ModelContextReducer ModelContextReducer
}

type boundTool struct {
	executable tool.Binding
	definition chat.ToolDefinition
	deferred   bool
	direct     bool
	concurrent ConcurrentTool
}

// Dispatcher executes model calls and ordinary Tool segments emitted by an
// Interaction Execution. Its configuration is immutable after construction;
// internal observation health counters are concurrency-safe. It may serve
// Processes concurrently when the supplied Client and Tools support concurrent use.
type Dispatcher struct {
	client                 ModelClient
	streamer               chat.Streamer
	tools                  map[string]boundTool
	delegates              map[string]struct{}
	initialDefinitions     []chat.ToolDefinition
	deferredToolNames      map[string]struct{}
	maxConcurrentToolCalls int
	observer               ExecutionObserver
	observationFailures    observationFailureCounters
	contextReducer         ModelContextReducer
}

// ObservationFailures returns a concurrency-safe snapshot of ExecutionObserver
// panics isolated by this Dispatcher. The counts do not alter settlements.
func (d *Dispatcher) ObservationFailures() ObservationFailureCounts {
	if d == nil {
		return ObservationFailureCounts{}
	}
	return d.observationFailures.snapshot()
}

// NewDispatcher binds an interaction definition to the model and tool
// implementations that execute its effects. The definition supplies the
// contract and the dispatcher supplies the I/O, which is what keeps a Step
// free of both.
func NewDispatcher(definition *Definition, config DispatcherConfig) (*Dispatcher, error) {
	if !definition.valid() || lo.IsNil(config.Client) {
		return nil, fmt.Errorf("%w: Definition and Client are required", ErrInvalidDispatcherConfig)
	}
	if config.MaxConcurrentToolCalls < 0 {
		return nil, fmt.Errorf("%w: MaxConcurrentToolCalls must not be negative", ErrInvalidDispatcherConfig)
	}
	if !config.ResponseMode.Valid() {
		return nil, fmt.Errorf("%w: invalid ResponseMode %q", ErrInvalidDispatcherConfig, config.ResponseMode)
	}
	if config.ModelContextReducer != nil && lo.IsNil(config.ModelContextReducer) {
		return nil, fmt.Errorf("%w: ModelContextReducer is typed nil", ErrInvalidDispatcherConfig)
	}
	maxConcurrentToolCalls := max(1, config.MaxConcurrentToolCalls)
	var streamer chat.Streamer
	if config.ResponseMode == ModelResponseStream {
		streamer, _ = config.Client.(chat.Streamer)
		if lo.IsNil(streamer) {
			return nil, fmt.Errorf("%w: Client does not support streaming", ErrInvalidDispatcherConfig)
		}
	}
	dispatcher := &Dispatcher{
		client:    config.Client,
		tools:     make(map[string]boundTool, len(config.Tools)+len(config.DeferredTools)),
		delegates: make(map[string]struct{}, len(definition.delegates)),
		initialDefinitions: make(
			[]chat.ToolDefinition, 0, len(config.Tools)+len(definition.delegates),
		),
		deferredToolNames:      make(map[string]struct{}, len(config.DeferredTools)),
		streamer:               streamer,
		maxConcurrentToolCalls: maxConcurrentToolCalls,
		observer:               config.Observer,
		contextReducer:         config.ModelContextReducer,
	}
	for index, executable := range config.Tools {
		if err := dispatcher.bindTool(executable, false); err != nil {
			return nil, fmt.Errorf("%w: Tools[%d]: %w", ErrInvalidDispatcherConfig, index, err)
		}
	}
	for index, executable := range config.DeferredTools {
		if err := dispatcher.bindTool(executable, true); err != nil {
			return nil, fmt.Errorf("%w: DeferredTools[%d]: %w", ErrInvalidDispatcherConfig, index, err)
		}
	}
	for _, delegate := range definition.delegates {
		name := delegate.definition.Name
		if _, duplicate := dispatcher.tools[name]; duplicate {
			return nil, fmt.Errorf("%w: Delegate name %q collides with a Tool", ErrInvalidDispatcherConfig, name)
		}
		dispatcher.delegates[name] = struct{}{}
		dispatcher.initialDefinitions = append(
			dispatcher.initialDefinitions, delegate.definition.Clone(),
		)
	}
	return dispatcher, nil
}

func (d *Dispatcher) bindTool(executable tool.Tool, deferred bool) error {
	binding, err := tool.Bind(executable)
	if err != nil {
		return err
	}
	definition := binding.Definition()
	if _, duplicate := d.tools[definition.Name]; duplicate {
		return fmt.Errorf("duplicate tool name %q", definition.Name)
	}
	direct, err := directResultCapability(executable)
	if err != nil {
		return err
	}
	concurrent, err := concurrentToolCapability(executable)
	if err != nil {
		return err
	}
	definition = definition.Clone()
	d.tools[definition.Name] = boundTool{
		executable: binding, definition: definition.Clone(), deferred: deferred,
		direct: direct, concurrent: concurrent,
	}
	if deferred {
		d.deferredToolNames[definition.Name] = struct{}{}
	} else {
		d.initialDefinitions = append(d.initialDefinitions, definition.Clone())
	}
	return nil
}

// Dispatch executes one validated Interaction protocol operation and returns a
// definite owner-defined Signal payload. An error means the external outcome
// is not provable; Engine therefore records an unknown settlement instead of
// retrying the operation.
func (d *Dispatcher) Dispatch(
	ctx context.Context,
	request agent.EffectRequest,
	emit agent.DeltaEmitter,
) (agent.Settlement, error) {
	if d == nil || lo.IsNil(d.client) {
		return agent.Settlement{}, ErrInvalidDispatcherConfig
	}
	envelope, err := decodeEffect(request.Effect().Payload())
	if err != nil {
		return agent.Settlement{}, err
	}
	switch envelope.Operation {
	case operationModelCall:
		return d.dispatchModel(ctx, request, envelope.ModelCall, emit)
	case operationToolBatch:
		return d.dispatchToolBatch(ctx, request, envelope.ToolBatch)
	default:
		return agent.Settlement{}, errors.New("interaction: unsupported dispatcher operation")
	}
}

// ReplayPolicy is deliberately conservative. Model calls may incur cost and
// produce a different answer, while Tools may have irreversible side effects;
// neither is replayed after a crash without an explicit Process resolution.
func (*Dispatcher) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	return agent.ReplayPolicyNever
}

func (d *Dispatcher) dispatchModel(
	ctx context.Context,
	request agent.EffectRequest,
	call *modelCall,
	emit agent.DeltaEmitter,
) (agent.Settlement, error) {
	modelRequest := call.Request.Clone()
	definitions, err := d.modelDefinitions(call.AdvertisedToolNames)
	if err != nil {
		return agent.Settlement{}, err
	}
	modelRequest.Tools = definitions
	if validateErr := modelRequest.Validate(); validateErr != nil {
		return agent.Settlement{}, fmt.Errorf("interaction: prepare model request: %w", validateErr)
	}
	invocation := modelInvocationFromRequest(
		request,
		call.ModelCallSequence,
		call.AppliedSteerSignalIDs,
	)
	ctx = withModelInvocation(ctx, invocation)
	if d.contextReducer != nil {
		effectiveMessages, reduceErr := d.contextReducer.ReduceModelContext(
			ctx, invocation, modelRequest.Clone(),
		)
		if reduceErr != nil {
			return modelHostFailureSettlement(
				request.ID(),
				fmt.Errorf("interaction: reduce model context: %w", reduceErr),
			)
		}
		modelRequest.Messages = cloneMessages(effectiveMessages)
		if validateErr := modelRequest.Validate(); validateErr != nil {
			return modelHostFailureSettlement(
				request.ID(),
				fmt.Errorf("interaction: reduced model context: %w", validateErr),
			)
		}
	}
	response, err := d.callModel(ctx, modelRequest, emit)
	if err != nil {
		if errors.Is(err, ErrHostFailure) {
			return modelHostFailureSettlement(request.ID(), err)
		}
		return modelFailureSettlement(request.ID(), err)
	}
	if response == nil {
		return modelFailureSettlement(request.ID(), errors.New("model returned a nil response"))
	}
	if validateErr := response.Validate(); validateErr != nil {
		return modelFailureSettlement(request.ID(), fmt.Errorf("invalid model response: %w", validateErr))
	}
	d.observeModel(ctx, invocation, response)
	payload, err := encodeProtocol(signalEnvelope{
		Operation: operationModelCall,
		ModelResult: &modelCallResult{
			Response:          response.Clone(),
			EffectiveMessages: cloneMessages(modelRequest.Messages),
		},
	})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(request.ID(), agent.SettlementStatusSucceeded, payload)
}

func (d *Dispatcher) modelDefinitions(advertisedToolNames []string) ([]chat.ToolDefinition, error) {
	if err := validateAdvertisedToolNames(advertisedToolNames); err != nil {
		return nil, fmt.Errorf("interaction: advertised Tools: %w", err)
	}
	definitions := cloneDefinitions(d.initialDefinitions)
	for _, name := range advertisedToolNames {
		binding, found := d.tools[name]
		if !found || !binding.deferred {
			return nil, fmt.Errorf("interaction: tool %q is not a bound deferred Tool", name)
		}
		definitions = append(definitions, binding.definition.Clone())
	}
	return definitions, nil
}

func modelFailureSettlement(effectID agent.EffectID, cause error) (agent.Settlement, error) {
	payload, err := encodeProtocol(signalEnvelope{
		Operation:   operationModelCall,
		ModelResult: &modelCallResult{Error: boundedDiagnostic(cause.Error())},
	})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(effectID, agent.SettlementStatusFailed, payload)
}

func modelHostFailureSettlement(effectID agent.EffectID, cause error) (agent.Settlement, error) {
	payload, err := encodeProtocol(signalEnvelope{
		Operation:   operationModelCall,
		ModelResult: &modelCallResult{HostError: boundedDiagnostic(cause.Error())},
	})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(effectID, agent.SettlementStatusFailed, payload)
}

func (d *Dispatcher) dispatchToolBatch(
	ctx context.Context,
	request agent.EffectRequest,
	batch *toolBatchCall,
) (agent.Settlement, error) {
	dispatch, err := newToolBatchDispatch(ctx, d, request, batch)
	if err != nil {
		return agent.Settlement{}, err
	}
	return dispatch.run()
}

type toolBatchDispatch struct {
	dispatcher          *Dispatcher
	ctx                 context.Context
	request             agent.EffectRequest
	batch               *toolBatchCall
	prepared            []preparedToolCall
	results             []chat.ToolResult
	advertisedToolNames []string
	start               int
	pauseCount          uint32
	continuation        *ToolInputContinuation
}

func newToolBatchDispatch(
	ctx context.Context,
	dispatcher *Dispatcher,
	request agent.EffectRequest,
	batch *toolBatchCall,
) (*toolBatchDispatch, error) {
	for _, call := range batch.Calls {
		if _, delegated := dispatcher.delegates[call.Name]; delegated {
			return nil, fmt.Errorf(
				"interaction: Delegate %q must be executed by the managed Execution boundary",
				call.Name,
			)
		}
	}
	dispatch := &toolBatchDispatch{
		dispatcher: dispatcher,
		ctx:        ctx,
		request:    request,
		batch:      batch,
		prepared:   dispatcher.prepareToolCalls(batch.Calls),
		results:    make([]chat.ToolResult, 0, len(batch.Calls)),
	}
	if batch.Checkpoint != nil {
		checkpoint := batch.Checkpoint
		dispatch.results = append(dispatch.results, cloneToolResults(checkpoint.CompletedResults)...)
		dispatch.advertisedToolNames = append(dispatch.advertisedToolNames, checkpoint.AdvertisedToolNames...)
		dispatch.start = int(checkpoint.NextToolCallIndex)
		dispatch.pauseCount = checkpoint.PauseCount
		if dispatch.pauseCount == ^uint32(0) {
			return nil, errors.New("interaction: tool checkpoint pause count exhausted")
		}
		dispatch.continuation = &ToolInputContinuation{
			state:    bytes.Clone(checkpoint.InputRequest.ContinuationState),
			response: bytes.Clone(batch.InputResponse),
		}
	}
	return dispatch, nil
}

func (t *toolBatchDispatch) run() (agent.Settlement, error) {
	if settlement, paused, err := t.resume(); err != nil || paused {
		return settlement, err
	}
	if settlement, paused, err := t.dispatchRemaining(); err != nil || paused {
		return settlement, err
	}
	return t.complete()
}

func (t *toolBatchDispatch) resume() (agent.Settlement, bool, error) {
	if t.continuation == nil {
		return agent.Settlement{}, false, nil
	}
	callContext := withToolInputContinuation(t.ctx, *t.continuation)
	result, newlyAdvertised, required, err := t.dispatcher.callTool(
		callContext,
		t.request,
		t.batch.ModelCallSequence,
		t.batch.FirstToolCallIndex+uint32(t.start),
		t.prepared[t.start],
	)
	if err != nil {
		return agent.Settlement{}, false, fmt.Errorf(
			"interaction: tool call %q: %w", t.batch.Calls[t.start].ID, err,
		)
	}
	if required != nil {
		settlement, pauseErr := t.pause(uint32(t.start), *required)
		return settlement, true, pauseErr
	}
	t.results = append(t.results, result)
	t.advertisedToolNames, err = mergeAdvertisedToolNames(
		t.advertisedToolNames, newlyAdvertised,
	)
	if err != nil {
		return agent.Settlement{}, false, err
	}
	t.start++
	return agent.Settlement{}, false, nil
}

func (t *toolBatchDispatch) dispatchRemaining() (agent.Settlement, bool, error) {
	plans, err := t.dispatcher.planToolConcurrency(t.prepared[t.start:])
	if err != nil {
		return agent.Settlement{}, false, err
	}
	for offset := 0; offset < len(plans); {
		end := offset + 1
		if t.dispatcher.maxConcurrentToolCalls > 1 {
			end = concurrentBatchEnd(plans, offset)
		}
		outcomes := t.dispatcher.callToolBatch(
			t.ctx,
			t.request,
			t.batch.ModelCallSequence,
			t.batch.FirstToolCallIndex+uint32(t.start+offset),
			t.prepared[t.start+offset:t.start+end],
		)
		for index, outcome := range outcomes {
			call := t.prepared[t.start+offset+index].call
			if outcome.err != nil {
				return agent.Settlement{}, false, fmt.Errorf("interaction: tool call %q: %w", call.ID, outcome.err)
			}
			if outcome.required != nil {
				if len(outcomes) != 1 {
					return agent.Settlement{}, false, fmt.Errorf(
						"interaction: concurrently executed tool call %q requested external input",
						call.ID,
					)
				}
				settlement, pauseErr := t.pause(uint32(t.start+offset), *outcome.required)
				return settlement, true, pauseErr
			}
		}
		for _, outcome := range outcomes {
			t.results = append(t.results, outcome.result)
			t.advertisedToolNames, err = mergeAdvertisedToolNames(
				t.advertisedToolNames,
				outcome.advertisedToolNames,
			)
			if err != nil {
				return agent.Settlement{}, false, err
			}
		}
		offset = end
	}
	return agent.Settlement{}, false, nil
}

func (t *toolBatchDispatch) pause(index uint32, request ToolInputRequest) (agent.Settlement, error) {
	if t.pauseCount == math.MaxUint32 {
		return agent.Settlement{}, errors.New("interaction: Tool input pause count is exhausted")
	}
	checkpoint := &toolCheckpoint{
		CompletedResults:    cloneToolResults(t.results),
		AdvertisedToolNames: slices.Clone(t.advertisedToolNames),
		NextToolCallIndex:   index,
		PauseCount:          t.pauseCount + 1,
		InputRequest:        wireInputRequest(request),
	}
	return toolCheckpointSettlement(t.request.ID(), checkpoint)
}

func (t *toolBatchDispatch) complete() (agent.Settlement, error) {
	direct := t.dispatcher.allCallsDirect(t.prepared) && !slices.ContainsFunc(t.results, func(result chat.ToolResult) bool {
		return result.IsError
	})
	payload, err := encodeProtocol(signalEnvelope{
		Operation: operationToolBatch,
		ToolResult: &toolBatchResult{
			Results: t.results, Direct: direct,
			AdvertisedToolNames: t.advertisedToolNames,
		},
	})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(t.request.ID(), agent.SettlementStatusSucceeded, payload)
}

func toolCheckpointSettlement(
	effectID agent.EffectID,
	checkpoint *toolCheckpoint,
) (agent.Settlement, error) {
	payload, err := encodeProtocol(signalEnvelope{
		Operation:  operationToolBatch,
		ToolResult: &toolBatchResult{Checkpoint: checkpoint},
	})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(effectID, agent.SettlementStatusSucceeded, payload)
}

func (d *Dispatcher) callTool(
	ctx context.Context,
	request agent.EffectRequest,
	modelCallSequence uint32,
	toolCallIndex uint32,
	prepared preparedToolCall,
) (
	result chat.ToolResult,
	advertisedToolNames []string,
	required *ToolInputRequest,
	err error,
) {
	call := prepared.call
	if prepared.rejection != nil {
		return prepared.rejection.Clone(), nil, nil, nil
	}
	invocation := toolInvocationFromRequest(
		request, modelCallSequence, toolCallIndex, call,
	)
	d.observeToolStarted(ctx, invocation)
	defer func() {
		settlement := ToolSettlement{}
		switch {
		case required != nil:
			settlement.InputRequired = true
		case result.ID != "":
			settlement.Result = &result
		case err != nil:
			settlement.Failure = boundedDiagnostic(err.Error())
			settlement.Unknown = true
		}
		d.observeToolSettled(ctx, invocation, settlement)
	}()
	binding := prepared.binding
	advertiser := newToolAdvertiser(d.deferredToolNames)
	ctx = withToolInvocation(ctx, invocation)
	ctx = withToolAdvertiser(ctx, advertiser)
	defer func() {
		if recovered := recover(); recovered != nil {
			result = chat.ToolResult{}
			advertisedToolNames = nil
			required = nil
			err = fmt.Errorf("tool panicked: %v", recovered)
		}
	}()
	output, err := binding.executable.Call(ctx, prepared.invocation)
	if isHostOrContextError(err) {
		return chat.ToolResult{}, nil, nil, err
	}
	if err != nil {
		if inputRequired, ok := errors.AsType[*ToolInputRequiredError](err); ok {
			request, valid := inputRequired.inputRequest()
			if !valid {
				return chat.ToolResult{}, nil, nil, ErrInvalidToolInputRequest
			}
			return chat.ToolResult{}, nil, &request, nil
		}
	}
	result, present := invocation.ModelResult(output, err)
	if !present {
		return chat.ToolResult{}, nil, nil, err
	}
	if result.IsError {
		return result, nil, nil, nil
	}
	return result, advertiser.advertisedNames(), nil, nil
}

func (d *Dispatcher) allCallsDirect(calls []preparedToolCall) bool {
	if len(calls) == 0 {
		return false
	}
	for _, call := range calls {
		if call.binding == nil || call.rejection != nil || !call.binding.direct {
			return false
		}
	}
	return true
}

func (d *Dispatcher) prepareToolCalls(calls []chat.ToolCall) []preparedToolCall {
	prepared := make([]preparedToolCall, len(calls))
	for index := range calls {
		call := calls[index]
		prepared[index].call = call
		binding, found := d.tools[call.Name]
		if !found {
			result := rejectedToolResult(call, fmt.Sprintf("tool %q is not available", call.Name))
			prepared[index].rejection = &result
			continue
		}
		prepared[index].binding = &binding
		invocation, err := binding.executable.Prepare(call)
		if err != nil {
			result := rejectedToolResult(call, "invalid arguments: "+boundedDiagnostic(err.Error()))
			prepared[index].rejection = &result
			continue
		}
		prepared[index].invocation = invocation
	}
	return prepared
}

func directResultCapability(executable tool.Tool) (direct bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			direct = false
			err = fmt.Errorf("direct-result capability panicked: %v", recovered)
		}
	}()
	capability, found, err := tool.Capability[DirectResultTool](executable)
	if err != nil {
		return false, fmt.Errorf("direct-result capability: %w", err)
	}
	if !found {
		return false, nil
	}
	return capability.ReturnsDirectResult(), nil
}

func concurrentToolCapability(executable tool.Tool) (declared ConcurrentTool, err error) {
	capability, found, err := tool.Capability[ConcurrentTool](executable)
	if err != nil {
		return nil, fmt.Errorf("concurrency capability: %w", err)
	}
	if !found {
		return nil, nil
	}
	return capability, nil
}

func cloneDefinitions(definitions []chat.ToolDefinition) []chat.ToolDefinition {
	cloned := make([]chat.ToolDefinition, len(definitions))
	for index := range definitions {
		cloned[index] = definitions[index].Clone()
	}
	return cloned
}

var _ agent.Dispatcher = (*Dispatcher)(nil)
