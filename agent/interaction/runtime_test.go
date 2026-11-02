package interaction

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

const interactionTestTimeout = 5 * time.Second
const objectToolSchema = `{"type":"object","properties":{"call":{"type":"string"}},"additionalProperties":false}`

type scriptTool struct {
	name       string
	invoke     func(context.Context, tool.Invocation) (tool.Result, error)
	direct     bool
	idempotent bool
	classify   func(tool.Invocation) (string, bool)
	advertise  func(context.Context, tool.Invocation, tool.Result) ([]string, error)
}

func (s scriptTool) Definition() tool.Definition {
	return tool.Definition{Name: s.name, Description: "Run the scripted operation.", InputSchema: json.RawMessage(objectToolSchema)}
}
func (s scriptTool) Call(ctx context.Context, input tool.Invocation) (tool.Result, error) {
	return s.invoke(ctx, input)
}
func (s scriptTool) ReturnsDirectResult() bool                               { return s.direct }
func (s scriptTool) Idempotent() bool                                        { return s.idempotent }
func (s scriptTool) ConcurrencyPolicy() func(tool.Invocation) (string, bool) { return s.classify }
func (s scriptTool) AdvertiseDeferred(ctx context.Context, input tool.Invocation, result tool.Result) ([]string, error) {
	if s.advertise == nil {
		return nil, nil
	}
	return s.advertise(ctx, input, result)
}

func mustValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
func testInput() Input {
	return Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("work"))}}
}
func finalResponse(text string) *chat.Response {
	return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart(text))), FinishReason: chat.FinishReasonStop}}
}
func callResponse(calls ...chat.ToolCall) *chat.Response {
	parts := make([]chat.Part, len(calls))
	for index, call := range calls {
		parts[index] = chat.NewToolCallPart(call)
	}
	return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(parts...)), FinishReason: chat.FinishReasonToolCalls}}
}

// requested echoes the model call ID into the arguments, the only call data a
// Tool receives, so scripted tools can tell sibling calls apart.
func requested(name, id string) chat.ToolCall {
	return chat.ToolCall{ID: id, Name: name, Arguments: `{"call":"` + id + `"}`}
}

func requestedCall(input tool.Invocation) string {
	var arguments struct {
		Call string `json:"call"`
	}
	if err := jsonv2.Unmarshal(input.Arguments(), &arguments); err != nil {
		panic(err)
	}
	return arguments.Call
}

func invocationEffect(input tool.Invocation) run.EffectID {
	return mustValue(run.ParseEffectID(input.IdempotencyKey()))
}
func compileTest(t testing.TB, runtime *run.Runtime, config Config) run.Program[Input, Output] {
	t.Helper()
	config.Name = "test.interaction"
	config.Description = "Exercise the interaction protocol."
	config.Revision = "test-model"
	program, err := Compile(runtime.Compiler(), config)
	if err != nil {
		t.Fatal(err)
	}
	return program
}
func toolSetTest(t testing.TB, tools ...tool.Tool) ToolSet {
	t.Helper()
	set, err := NewToolSet(ToolSetConfig{Revision: "test-tools", Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	return set
}
func startInteraction(t *testing.T, runtime *run.Runtime, program run.Program[Input, Output]) *run.Handle[Output] {
	t.Helper()
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[Input]{ID: run.StartID{Scope: "test", Key: strings.ReplaceAll(t.Name(), "/", "-")}, Input: testInput()})
	if err != nil {
		t.Fatal(err)
	}
	return handle
}
func awaitOutput(t *testing.T, handle *run.Handle[Output]) Output {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), interactionTestTimeout)
	defer cancel()
	result, err := handle.Await(ctx)
	if err != nil {
		t.Fatal(err)
	}
	output, err := result.Output()
	if err != nil {
		t.Fatalf("termination=%+v: %v", result.Termination(), err)
	}
	return output
}
func awaitLog(t *testing.T, store run.Store, root run.ProcessID, accept func(run.Log) bool) run.Log {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), interactionTestTimeout)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		log, found, err := store.Load(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		if found && accept(log) {
			return log
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return run.Log{}
		case <-ticker.C:
		}
	}
}
func completedLog(t *testing.T, store run.Store, root run.ProcessID) run.Log {
	t.Helper()
	log, found, err := store.Load(t.Context(), root)
	if err != nil || !found {
		t.Fatalf("log found=%v error=%v", found, err)
	}
	return log
}

func TestToolsUseDirectEffectsAndCommittedOutcomes(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	var calls []run.EffectID
	executable := scriptTool{name: "echo", invoke: func(_ context.Context, input tool.Invocation) (tool.Result, error) {
		calls = append(calls, invocationEffect(input))
		return tool.Result{Output: tool.NewTextOutput(requestedCall(input))}, nil
	}}
	models := 0
	model := chat.ModelFunc(func(ctx context.Context, request *chat.Request) (*chat.Response, error) {
		models++
		if models == 1 {
			return callResponse(requested("echo", "first"), requested("echo", "second")), nil
		}
		var ids []string
		for _, part := range request.Messages[len(request.Messages)-1].Parts {
			if part.ToolResult != nil {
				ids = append(ids, part.ToolResult.ID)
			}
		}
		if !slices.Equal(ids, []string{"first", "second"}) {
			return nil, errors.New("result order changed")
		}
		return finalResponse("done"), nil
	})
	program := compileTest(t, runtime, Config{Model: model, Tools: toolSetTest(t, executable)})
	handle := startInteraction(t, runtime, program)
	output := awaitOutput(t, handle)
	if output.ModelCalls != 2 || len(calls) != 2 {
		t.Fatalf("models=%d tools=%d", output.ModelCalls, len(calls))
	}
	log := completedLog(t, store, handle.ID())
	starts := 0
	var intents []run.EffectID
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case run.Started:
			starts++
		case run.EffectIntent:
			intents = append(intents, record.ID)
			if record.ID.Process != handle.ID() || record.Effect.Target() != run.EffectTargetDispatcher {
				t.Fatal("ordinary tool allocated a child lifecycle")
			}
		}
	}
	if starts != 1 || len(intents) != 4 {
		t.Fatalf("starts=%d intents=%d", starts, len(intents))
	}
	if !slices.Equal(calls, intents[1:3]) {
		t.Fatalf("idempotency keys %v are not the committed tool intents %v", calls, intents)
	}
	rounds, err := SettledResults(program, log)
	if err != nil || len(rounds) != 1 || !rounds[0].Complete() {
		t.Fatalf("rounds=%+v err=%v", rounds, err)
	}
	if rounds[0].ProcessID() != handle.ID() || rounds[0].ModelEffectID() != intents[0] || rounds[0].ModelCallSequence() != 1 || rounds[0].CallCount() != 2 || !rounds[0].Digest().Valid() {
		t.Fatal("round attribution is not derived from its model intent")
	}
	data := rounds[0].JSON()
	data[0] = '['
	if rounds[0].JSON()[0] != '{' {
		t.Fatal("round JSON aliases mutable caller memory")
	}
	for index, entry := range rounds[0].Entries() {
		reference, ok := rounds[0].Reference(uint32(index))
		if !ok || reference != calls[index] || entry.Reference != reference {
			t.Fatal("result reference not derived from committed intent")
		}
	}
}

func TestContinuationsUseFreshWaitKeysAndOneLogicalReference(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store, CheckpointInterval: 1})
	var keys []string
	executable := scriptTool{name: "ask", direct: true, invoke: func(_ context.Context, input tool.Invocation) (tool.Result, error) {
		keys = append(keys, input.IdempotencyKey())
		continuation, resumed := input.Continuation()
		stage := 0
		if resumed {
			if err := jsonv2.Unmarshal(continuation.State(), &stage); err != nil {
				return tool.Result{}, err
			}
			if string(continuation.Response()) != `"answer"` {
				return tool.Result{}, errors.New("answer changed")
			}
		}
		if stage == 2 {
			return tool.Result{Output: tool.NewTextOutput("confirmed")}, nil
		}
		request, err := newInputRequest([]byte(`"question"`), []byte(`{"type":"string"}`), []byte{byte('1' + stage)})
		return tool.Result{InputRequest: &request}, err
	}}
	program := compileTest(t, runtime, Config{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("ask", "approval")), nil
	}), Tools: toolSetTest(t, executable)})
	handle := startInteraction(t, runtime, program)
	var previous run.WaitKey
	for range 2 {
		var pending PendingToolInput
		awaitLog(t, store, handle.ID(), func(log run.Log) bool {
			inputs, err := PendingToolInputs(program, log)
			if err != nil {
				t.Fatal(err)
			}
			if len(inputs) != 1 || inputs[0].WaitKey() == previous {
				return false
			}
			pending = inputs[0]
			return true
		})
		if string(pending.Prompt()) != `"question"` || string(pending.ResponseSchema()) != `{"type":"string"}` {
			t.Fatal("input projection lost its question contract")
		}
		response := mustValue(pending.ResponseSignal(mustValue(run.ParseSignalID("valid-answer")), []byte(`"answer"`)))
		if response.Wait != pending.WaitKey() || string(response.Payload.JSON()) != `"answer"` {
			t.Fatal("answer lost wait ownership")
		}
		if pending.ProcessID() != handle.ID() {
			t.Fatal("input belongs to a synthetic tool process")
		}
		if _, err := pending.ResponseSignal(mustValue(run.ParseSignalID("invalid")), []byte(`4`)); err == nil {
			t.Fatal("wrong answer schema admitted")
		}
		if err := handle.Answer(t.Context(), pending.WaitKey(), "answer"); err != nil {
			t.Fatal(err)
		}
		if err := handle.Answer(t.Context(), pending.WaitKey(), "answer"); err != nil {
			t.Fatalf("identical answer retry: %v", err)
		}
		previous = pending.WaitKey()
	}
	output := awaitOutput(t, handle)
	if len(output.DirectToolResults) != 1 || output.ModelCalls != 1 || len(keys) != 3 {
		t.Fatalf("output=%+v invocations=%d", output, len(keys))
	}
	if first := mustValue(run.ParseEffectID(keys[0])); first.Process != handle.ID() || slices.ContainsFunc(keys, func(key string) bool { return key != keys[0] }) {
		t.Fatalf("continuations changed the logical call key: %v", keys)
	}
	pending, err := PendingToolInputs(program, completedLog(t, store, handle.ID()))
	if err != nil || len(pending) != 0 {
		t.Fatalf("finished input remains: %+v %v", pending, err)
	}
}

func TestUnknownToolEvidenceRequiresExplicitResolution(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	var count atomic.Int32
	executable := scriptTool{name: "write", invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		count.Add(1)
		cause, err := tool.NewCallError(tool.CallErrorConfig{Cause: errors.New("acknowledgment lost"), Evidence: tool.NewTextOutput("remote accepted")})
		if err != nil {
			return tool.Result{}, err
		}
		return tool.Result{}, cause
	}}
	models := 0
	program := compileTest(t, runtime, Config{Tools: toolSetTest(t, executable), Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		models++
		if models == 1 {
			return callResponse(requested("write", "operation")), nil
		}
		return finalResponse("resolved"), nil
	})})
	handle := startInteraction(t, runtime, program)
	var unknown run.EffectSettled
	log := awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		for _, entry := range log.Entries {
			if settled, ok := entry.Record.(run.EffectSettled); ok && settled.Outcome.Status() == run.SettlementStatusUnknown {
				unknown = settled
				return true
			}
		}
		return false
	})
	if unknown.Failure != nil {
		t.Fatal("owner-defined uncertainty was replaced by a runtime fault")
	}
	evidence, err := ToolSettlementFromOutcome(requested("write", "operation"), unknown.Outcome)
	if err != nil || evidence.Evidence == nil || evidence.Result != nil {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	inspection, err := log.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	process, _ := inspection.Process(handle.ID())
	if len(process.PendingEffects) != 1 {
		t.Fatalf("pending=%v", process.PendingEffects)
	}
	request := process.PendingEffects[0].Request
	settlement, err := SettleToolResult(program, request, tool.Result{Output: tool.NewTextOutput("verified")}, ResultSucceeded, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.ResolveUnknownEffect(t.Context(), unknown.ID, settlement); err != nil {
		t.Fatal(err)
	}
	awaitOutput(t, handle)
	if count.Load() != 1 {
		t.Fatalf("unknown operation executed %d times", count.Load())
	}
}

func TestConcurrentResultsPreserveProposalOrder(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	started := make(chan string, 2)
	release := map[string]chan struct{}{"one": make(chan struct{}), "two": make(chan struct{})}
	makeTool := func(name string) scriptTool {
		return scriptTool{name: name, direct: true, classify: func(tool.Invocation) (string, bool) { return "", true }, invoke: func(ctx context.Context, _ tool.Invocation) (tool.Result, error) {
			started <- name
			select {
			case <-ctx.Done():
				return tool.Result{}, ctx.Err()
			case <-release[name]:
				return tool.Result{Output: tool.NewTextOutput(name)}, nil
			}
		}}
	}
	program := compileTest(t, runtime, Config{MaxConcurrentToolCalls: 2, Tools: toolSetTest(t, makeTool("one"), makeTool("two")), Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("one", "first"), requested("two", "second")), nil
	})})
	handle := startInteraction(t, runtime, program)
	ctx, cancel := context.WithTimeout(t.Context(), interactionTestTimeout)
	defer cancel()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(release["two"])
	close(release["one"])
	output := awaitOutput(t, handle)
	if output.DirectToolResults[0].ID != "first" || output.DirectToolResults[1].ID != "second" {
		t.Fatal("completion order escaped into business order")
	}
}

func TestAdmissionRejectsWholeProposalBeforeAnyToolEffect(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	var called atomic.Bool
	executable := scriptTool{name: "known", invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		called.Store(true)
		return tool.Result{}, nil
	}}
	models := 0
	program := compileTest(t, runtime, Config{Tools: toolSetTest(t, executable), Model: chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		models++
		if models == 1 {
			return callResponse(requested("known", "first"), requested("absent", "second")), nil
		}
		for _, part := range request.Messages[len(request.Messages)-1].Parts {
			if part.ToolResult == nil || !part.ToolResult.IsError {
				return nil, errors.New("proposal rejection missing")
			}
		}
		return finalResponse("corrected"), nil
	})})
	handle := startInteraction(t, runtime, program)
	awaitOutput(t, handle)
	if called.Load() {
		t.Fatal("valid prefix executed before later invalid proposal")
	}
	for _, entry := range completedLog(t, store, handle.ID()).Entries {
		if intent, ok := entry.Record.(run.EffectIntent); ok {
			_, _, toolCall, err := ToolCallFromEffect(intent.Effect)
			if err != nil || toolCall {
				t.Fatalf("rejected proposal emitted tool intent: %v", err)
			}
		}
	}
}

func TestDelegatesComposeThroughSpawnExitAndArtifactAdmission(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	type request struct {
		Value int `json:"value"`
	}
	type response struct {
		Value int `json:"value"`
	}
	stage, err := workflow.Transform("double", func(_ context.Context, input request) (response, error) { return response{Value: input.Value * 2}, nil })
	if err != nil {
		t.Fatal(err)
	}
	childProgram, err := workflow.Compile[request, response](runtime.Compiler(), workflow.Config{Name: "test.worker", Description: "Double a value.", Revision: "double", Stages: []workflow.Stage{stage}})
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := NewDelegate(DelegateConfig{Name: "worker", Description: "Double the requested value.", Program: childProgram.Erase()})
	if err != nil {
		t.Fatal(err)
	}
	models := 0
	program := compileTest(t, runtime, Config{Delegates: []Delegate{delegate}, Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		models++
		if models == 1 {
			return callResponse(chat.ToolCall{ID: "delegation", Name: "worker", Arguments: `{"value":3}`}), nil
		}
		return finalResponse("six"), nil
	}), CompletionValidator: func(_ context.Context, candidate CompletionCandidate) (CompletionDecision, error) {
		artifacts := candidate.Artifacts()
		if len(artifacts) != 1 {
			return CompletionDecision{}, errors.New("missing delegate artifact")
		}
		value, decodeErr := artifacts[0].Output().Decode[response]()
		if decodeErr != nil || value.Value != 6 {
			return CompletionDecision{}, errors.New("artifact output changed")
		}
		return CompletionDecision{Accepted: true}, nil
	}})
	handle := startInteraction(t, runtime, program)
	awaitOutput(t, handle)
	log := completedLog(t, store, handle.ID())
	attributed, err := DelegateAttributions(program, log)
	if err != nil || len(attributed) != 1 || attributed[0].ProcessID().Depth() != 1 {
		t.Fatalf("attribution=%+v err=%v", attributed, err)
	}
	for _, attribution := range attributed {
		key, _ := attribution.ProcessID().Key()
		reference, found := attribution.Reference()
		if !found || !attribution.Valid() || attribution.ModelCallSequence() != 1 || attribution.ToolCallIndex() != 0 || attribution.ToolCall().ID != "delegation" || attribution.ChildKey() != key || reference.Process != handle.ID() {
			t.Fatal("delegation attribution lost its intent or child identity")
		}
	}
	rounds, err := SettledResults(program, log)
	if err != nil || len(rounds) != 1 || !rounds[0].Complete() {
		t.Fatalf("rounds=%+v err=%v", rounds, err)
	}
	inspection, err := log.Inspect()
	if err != nil || len(inspection.Processes) != 1 {
		t.Fatalf("child not reaped: %+v %v", inspection, err)
	}
}

func TestDirectEffectRestorationAndExplicitCoordinates(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	executable := scriptTool{name: "echo", direct: true, invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		return tool.Result{Output: tool.NewTextOutput("answer")}, nil
	}}
	program := compileTest(t, runtime, Config{Tools: toolSetTest(t, executable), Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return finalResponse("unused"), nil })})
	definition := program.Definition()
	input, err := run.EncodePayload(testInput())
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := execution.Step(t.Context(), testTurn(1))
	if err != nil || first.Kind() != run.TransitionKindYield {
		t.Fatalf("first=%v %v", first, err)
	}
	response, err := (modelCallResult{Response: callResponse(requested("echo", "call"))}).settlement(run.MaxPayloadBytes)
	if err != nil {
		t.Fatal(err)
	}
	second := testTurn(2)
	second.Messages = []run.Message{run.Completion{Effect: testTurn(1).EffectID(0), Outcome: response}}
	if _, stepErr := execution.Step(t.Context(), second); stepErr != nil {
		t.Fatal(stepErr)
	}
	third := testTurn(3)
	effects, err := execution.Step(t.Context(), third)
	if err != nil || len(effects.Effects()) != 1 {
		t.Fatalf("tool effects=%v err=%v", effects, err)
	}
	state, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := (toolDispatchResult{Completion: &toolCallResult{Disposition: ResultSucceeded, Output: tool.NewTextOutput("answer")}}).settlement()
	if err != nil {
		t.Fatal(err)
	}
	fourth := testTurn(4)
	fourth.Messages = []run.Message{run.Completion{Effect: third.EffectID(0), Outcome: settlement}}
	runtest.RunDefinitionConformance(t, runtest.DefinitionConformanceConfig{Definition: definition, Input: input, InitialTurn: testTurn(1), FollowingTurns: []run.Turn{second, third, fourth, testTurn(5)}, RestoredCases: []runtest.ExecutionConformanceCase{{Name: "tool completion", State: state, Turn: fourth, FollowingTurns: []run.Turn{testTurn(5)}}}})
}

func TestCompileOwnsConfigurationIdentityAndExactModelRequest(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return finalResponse("done"), nil })
	first := compileTest(t, runtime, Config{Model: model, MaxModelCalls: run.NewQuota(2)})
	second := compileTest(t, runtime, Config{Model: model, MaxModelCalls: run.NewQuota(3)})
	if first.ProgramRef() == second.ProgramRef() {
		t.Fatal("compiler omitted model policy")
	}
	invocation, err := first.Definition().Start(mustValue(run.EncodePayload(testInput())))
	if err != nil {
		t.Fatal(err)
	}
	transition, err := invocation.Step(t.Context(), testTurn(1))
	if err != nil {
		t.Fatal(err)
	}
	request, found, err := ModelRequestFromEffect(transition.Effects()[0])
	if err != nil || !found || !reflect.DeepEqual(request.Messages, testInput().Messages) {
		t.Fatalf("request=%+v found=%v err=%v", request, found, err)
	}
}
