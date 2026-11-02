package interaction

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

func replaceLoggedEffect(t *testing.T, log run.Log, id run.EffectID, effect run.Effect) run.Log {
	t.Helper()
	copy := mustValue(log.Clone())
	for index, entry := range copy.Entries {
		switch record := entry.Record.(type) {
		case run.EffectIntent:
			if record.ID == id {
				record.Effect = effect
				copy.Entries[index].Record = record
			}
		case run.Stepped:
			if record.Process != id.Process || record.Step != id.Step {
				continue
			}
			effects := record.Transition.Effects()
			effects[id.Index] = effect
			if record.Transition.Kind() == run.TransitionKindYield {
				record.Transition = mustValue(run.Yield(record.Transition.Consumed(), effects, record.Transition.Waits()...))
			} else {
				record.Transition = mustValue(run.Continue(record.Transition.Consumed(), effects...))
			}
			copy.Entries[index].Record = record
		}
	}
	if err := copy.Validate(); err != nil {
		t.Fatal("fixture changed the kernel log contract", err)
	}
	return copy
}

func replaceLoggedOutcome(t *testing.T, log run.Log, id run.EffectID, outcome run.Settlement) run.Log {
	t.Helper()
	copy := mustValue(log.Clone())
	for index, entry := range copy.Entries {
		if record, ok := entry.Record.(run.EffectSettled); ok && record.ID == id {
			record.Outcome = outcome
			copy.Entries[index].Record = record
		}
	}
	if err := copy.Validate(); err != nil {
		t.Fatal("fixture changed the kernel's settlement contract", err)
	}
	return copy
}

func requestFromIntentPrefix(t *testing.T, log run.Log, id run.EffectID) run.EffectRequest {
	t.Helper()
	for index, entry := range log.Entries {
		if intent, ok := entry.Record.(run.EffectIntent); ok && intent.ID == id {
			log.Entries = log.Entries[:index+1]
			view := mustValue(log.Inspect())
			process, found := view.Process(id.Process)
			if found {
				for _, pending := range process.PendingEffects {
					if pending.Request.ID() == id {
						return pending.Request
					}
				}
			}
			break
		}
	}
	t.Fatal("committed intent did not produce its pending request")
	return run.EffectRequest{}
}

func TestOneProviderCallCannotAcquireTwoInitialEffectOwners(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	program := compileTest(t, runtime, Config{Tools: toolSetTest(t, scriptTool{name: "echo", direct: true, invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		return tool.Result{Output: tool.NewTextOutput("done")}, nil
	}}), Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("echo", "first"), requested("echo", "second")), nil
	})})
	handle := startInteraction(t, runtime, program)
	awaitOutput(t, handle)
	log := completedLog(t, store, handle.ID())
	var intents []run.EffectIntent
	for _, entry := range log.Entries {
		if intent, ok := entry.Record.(run.EffectIntent); ok {
			if _, _, recognized, _ := ToolCallFromEffect(intent.Effect); recognized {
				intents = append(intents, intent)
			}
		}
	}
	if len(intents) != 2 {
		t.Fatal("fixture did not issue two distinct direct tool effects")
	}
	question := mustValue(newInputRequest([]byte(`"Approve?"`), []byte(`{"type":"boolean"}`), []byte(`null`)))
	for _, resume := range []*toolResume{nil, {Reference: intents[0].ID, InputRequest: question, InputResponse: []byte(`true`)}} {
		envelope := mustValue(newToolEffect(toolDispatchRequest{Invocation: toolCall{Call: requested("echo", "first")}, Resume: resume}))
		changed := mustValue(run.NewDispatcherEffect(mustValue(jsonv2.Marshal(envelope))))
		corrupt := replaceLoggedEffect(t, log, intents[1].ID, changed)
		assertProjectionRejection(t, program, corrupt)
	}
}

func TestContinuationProjectionRequiresTheExactOutstandingQuestion(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	question := mustValue(newInputRequest([]byte(`"Approve?"`), []byte(`{"type":"boolean"}`), []byte(`{"checkpoint":1}`)))
	program := compileTest(t, runtime, Config{Tools: toolSetTest(t, scriptTool{name: "ask", direct: true, invoke: func(_ context.Context, invocation tool.Invocation) (tool.Result, error) {
		if _, resumed := invocation.Continuation(); !resumed {
			return tool.Result{InputRequest: &question}, nil
		}
		return tool.Result{Output: tool.NewTextOutput("confirmed")}, nil
	}}), Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("ask", "approval")), nil
	})})
	handle := startInteraction(t, runtime, program)
	var pending PendingToolInput
	awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		inputs := mustValue(PendingToolInputs(program, log))
		if len(inputs) == 1 {
			pending = inputs[0]
			return true
		}
		return false
	})
	if err := handle.Answer(t.Context(), pending.WaitKey(), true); err != nil {
		t.Fatal(err)
	}
	awaitOutput(t, handle)
	log := completedLog(t, store, handle.ID())
	var intent run.EffectIntent
	for _, entry := range log.Entries {
		if candidate, ok := entry.Record.(run.EffectIntent); ok && candidate.Effect.Target() == run.EffectTargetDispatcher {
			envelope := mustValue(decodeEffect(candidate.Effect.Payload()))
			if envelope.ToolCall != nil && envelope.ToolCall.Resume != nil {
				intent = candidate
			}
		}
	}
	if !intent.ID.Valid() {
		t.Fatal("fixture did not emit a continuation")
	}
	for _, change := range []func(*toolDispatchRequest){
		func(request *toolDispatchRequest) { request.Invocation.Call.ID = "another-provider-call" },
		func(request *toolDispatchRequest) {
			request.Resume.InputRequest = mustValue(newInputRequest([]byte(`"Changed question?"`), question.ResponseSchema().JSON(), question.State()))
		},
		func(request *toolDispatchRequest) {
			request.Resume.InputRequest = mustValue(newInputRequest(question.Prompt(), question.ResponseSchema().JSON(), []byte(`{"checkpoint":2}`)))
		},
		func(request *toolDispatchRequest) { request.Resume.Reference = intent.ID },
	} {
		envelope := mustValue(decodeEffect(intent.Effect.Payload()))
		change(envelope.ToolCall)
		effect := mustValue(run.NewDispatcherEffect(mustValue(jsonv2.Marshal(envelope))))
		assertProjectionRejection(t, program, replaceLoggedEffect(t, log, intent.ID, effect))
	}
}

func TestPendingToolQuestionMustReflectItsCommittedInputRequest(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	question := mustValue(newInputRequest([]byte(`"Approve?"`), []byte(`{"type":"boolean"}`), []byte(`{"private":true}`)))
	program := compileTest(t, runtime, Config{Tools: toolSetTest(t, scriptTool{name: "ask", invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		return tool.Result{InputRequest: &question}, nil
	}}), Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("ask", "approval")), nil
	})})
	handle := startInteraction(t, runtime, program)
	log := awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		return len(mustValue(PendingToolInputs(program, log))) == 1
	})
	for _, row := range []struct {
		name   string
		change func(*run.Wait)
	}{
		{"prompt", func(wait *run.Wait) { wait.Question = mustValue(run.EncodePayload("another question")) }},
		{"answer contract", func(wait *run.Wait) { wait.AnswerSchema = mustValue(jsonschema.For[string]()) }},
		{"answer bound", func(wait *run.Wait) { wait.MaxAnswerBytes = tool.MaxInputBytes / 2 }},
		{"identity", func(wait *run.Wait) { wait.Key = mustValue(run.ParseWaitKey("unrelated")) }},
	} {
		t.Run(row.name, func(t *testing.T) {
			corrupt := mustValue(log.Clone())
			for index, entry := range corrupt.Entries {
				switch record := entry.Record.(type) {
				case run.Stepped:
					waits := record.Transition.Waits()
					if record.Process != handle.ID() || len(waits) == 0 {
						continue
					}
					row.change(&waits[0])
					record.Transition = mustValue(run.Yield(record.Transition.Consumed(), record.Transition.Effects(), waits...))
					corrupt.Entries[index].Record = record
				case run.Checkpoint:
					for process := range record.Processes {
						if record.Processes[process].Started.Process == handle.ID() && len(record.Processes[process].Waits) > 0 {
							row.change(&record.Processes[process].Waits[0])
						}
					}
					corrupt.Entries[index].Record = record
				}
			}
			if err := corrupt.Validate(); err != nil {
				t.Fatal("fixture changed a kernel-owned contract", err)
			}
			if _, err := PendingToolInputs(program, corrupt); !errors.Is(err, ErrInvalidResult) {
				t.Fatal("wait projection originated a different tool question", err)
			}
		})
	}
}

func TestDelegateIntentProjectionRequiresTheExactDeclaredBinding(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store, ProcessAdmitter: run.ProcessAdmitterFunc(func(ctx context.Context, admission run.ProcessAdmission) error {
		if admission.ProcessID().Depth() > 0 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})})
	delegate := compiledDelegate(t, runtime.Compiler(), "worker", func(_ context.Context, value delegateValue) (delegateValue, error) { return value, nil })
	foreign := compiledDelegate(t, runtime.Compiler(), "foreign", func(_ context.Context, value delegateValue) (delegateValue, error) { return value, nil })
	program := compileTest(t, runtime, Config{Delegates: []Delegate{delegate}, Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(chat.ToolCall{ID: "delegation", Name: "worker", Arguments: `{"value":3}`}), nil
	})})
	handle := startInteraction(t, runtime, program)
	var intent run.EffectIntent
	log := awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		for _, entry := range log.Entries {
			if candidate, ok := entry.Record.(run.EffectIntent); ok && candidate.Effect.Target() == run.EffectTargetFramework {
				intent = candidate
				return true
			}
		}
		return false
	})
	for _, row := range []struct {
		name   string
		change func(*run.ChildSpec)
	}{
		{"child key", func(spec *run.ChildSpec) { spec.Key = mustValue(run.ParseChildKey("undeclared")) }},
		{"program", func(spec *run.ChildSpec) { spec.ProgramRef = foreign.program.ProgramRef() }},
		{"input", func(spec *run.ChildSpec) { spec.Input = mustValue(run.EncodePayload(delegateValue{Value: 4})) }},
		{"budget", func(spec *run.ChildSpec) { spec.Budget.Steps = run.NewQuota(1) }},
		{"capabilities", func(spec *run.ChildSpec) {
			spec.Capabilities = mustValue(run.NewCapabilitySet(mustValue(run.ParseCapability("foreign.write"))))
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			spec := mustValue(run.ParseChildStartEffect(intent.Effect))
			row.change(&spec)
			changed := replaceLoggedEffect(t, log, intent.ID, mustValue(run.NewChildStartEffect(spec)))
			assertProjectionRejection(t, program, changed)
		})
	}
	spec := mustValue(run.ParseChildStartEffect(intent.Effect))
	child := mustValue(handle.ID().Child(spec.Key))
	control := mustValue(run.NewChildCancelEffect(child, "not a delegated start"))
	assertProjectionRejection(t, program, replaceLoggedEffect(t, log, intent.ID, control))
}

func TestDelegateResultProjectionRequiresTheDeclaredOutputContract(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store, CheckpointInterval: 1000})
	delegate := compiledDelegate(t, runtime.Compiler(), "worker", func(_ context.Context, value delegateValue) (delegateValue, error) { return value, nil })
	models := 0
	program := compileTest(t, runtime, Config{Delegates: []Delegate{delegate}, Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		models++
		if models == 1 {
			return callResponse(chat.ToolCall{ID: "delegation", Name: "worker", Arguments: `{"value":3}`}), nil
		}
		return finalResponse("done"), nil
	})})
	handle := startInteraction(t, runtime, program)
	awaitOutput(t, handle)
	log := completedLog(t, store, handle.ID())
	if rounds := mustValue(SettledResults(program, log)); len(rounds) != 1 || !rounds[0].Complete() {
		t.Fatal("valid delegated output was not projected")
	}
	corrupt := mustValue(log.Clone())
	output := mustValue(run.EncodePayload("wrong output type"))
	termination := mustValue(mustValue(run.ParsePayload([]byte(`{"output":"wrong output type"}`))).Decode[run.Termination]())
	for index, entry := range corrupt.Entries {
		switch record := entry.Record.(type) {
		case run.Stepped:
			if record.Process.Depth() > 0 && record.Transition.Kind() == run.TransitionKindComplete {
				record.Transition = mustValue(run.Complete(record.Transition.Consumed(), output))
				corrupt.Entries[index].Record = record
			}
		case run.Exited:
			if record.Process.Depth() > 0 {
				record.Termination = termination
				corrupt.Entries[index].Record = record
			}
		}
	}
	if err := corrupt.Validate(); err != nil {
		t.Fatal("fixture changed the kernel's lifecycle contract", err)
	}
	assertProjectionRejection(t, program, corrupt)
}

func TestToolProjectionRequiresTheExactProposalAndOutcomeVariant(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store, CheckpointInterval: 1000})
	program := compileTest(t, runtime, Config{ContextProcessor: contextProcessorFunc(func(_ context.Context, _ ContextInvocation, request *chat.Request) ([]chat.Message, error) {
		return request.Messages, nil
	}), Tools: toolSetTest(t, scriptTool{name: "echo", direct: true, invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		return tool.Result{Output: tool.NewTextOutput("done")}, nil
	}}), Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("echo", "call")), nil
	})})
	handle := startInteraction(t, runtime, program)
	awaitOutput(t, handle)
	log := completedLog(t, store, handle.ID())
	var model, invocation run.EffectIntent
	for _, entry := range log.Entries {
		if intent, ok := entry.Record.(run.EffectIntent); ok {
			if _, _, isTool, _ := ToolCallFromEffect(intent.Effect); isTool {
				invocation = intent
			} else if _, isModel, _ := ModelRequestFromEffect(intent.Effect); isModel {
				model = intent
			}
		}
	}
	for _, row := range []struct {
		call            chat.ToolCall
		recoveryAllowed bool
	}{
		{requested("echo", "unproposed"), true},
		{requested("foreign", "call"), false},
		{chat.ToolCall{ID: "call", Name: "echo", Arguments: `{"changed":true}`}, false},
	} {
		effect := mustValue(toolEffect(toolDispatchRequest{Invocation: toolCall{Call: row.call}}))
		changed := replaceLoggedEffect(t, log, invocation.ID, effect)
		assertProjectionRejection(t, program, changed)
		request := requestFromIntentPrefix(t, changed, invocation.ID)
		settlement, err := SettleToolResult(program, request, tool.Result{}, ResultSucceeded, nil)
		if row.recoveryAllowed {
			if err != nil || !settlement.Valid() {
				t.Fatal("captured valid tool contract rejected an investigated result", err)
			}
		} else if !errors.Is(err, ErrInvalidProtocol) || settlement.Valid() {
			t.Fatal("investigated result bypassed the captured tool contract", err)
		}
	}
	malformed := mustValue(run.NewDispatcherEffect([]byte(`{"unknown_operation":true}`)))
	malformedLog := replaceLoggedEffect(t, log, invocation.ID, malformed)
	assertProjectionRejection(t, program, malformedLog)
	malformedRequest := requestFromIntentPrefix(t, malformedLog, invocation.ID)
	if _, err := ToolInvocationFromRequest(run.EffectRequest{}); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("tool invocation fabricated a missing request", err)
	}
	if _, err := ToolInvocationFromRequest(malformedRequest); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("malformed persisted intent acquired tool attribution", err)
	}
	if _, err := SettleModelResult(program, malformedRequest, finalResponse("replacement")); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("malformed persisted intent acquired model recovery authority", err)
	}
	if _, err := SettlePreparedContext(program, malformedRequest, testInput().Messages); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("malformed intent acquired context recovery authority", err)
	}
	modelRequest := requestFromIntentPrefix(t, log, model.ID)
	if _, err := SettlePreparedContext(program, modelRequest, testInput().Messages); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("model request acquired context recovery authority", err)
	}
	if _, err := SettleToolResult(program, modelRequest, tool.Result{}, ResultSucceeded, nil); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("model request acquired tool recovery authority", err)
	}
	beforeProposal := mustValue(toolEffect(toolDispatchRequest{Invocation: toolCall{Call: requested("echo", "call")}}))
	assertProjectionRejection(t, program, replaceLoggedEffect(t, log, model.ID, beforeProposal))
	for _, wire := range []string{
		`{}`,
		`{"tool_result":{"completion":{"disposition":"succeeded","output":{"content":[{"kind":"invalid"}]}}}}`,
		`{"prepared_context":{"messages":[{"role":"user","parts":[{"kind":"text","text":"wrong operation"}]}]}}`,
	} {
		outcome := mustValue(run.NewSettlement(run.SettlementStatusSucceeded, []byte(wire)))
		assertProjectionRejection(t, program, replaceLoggedOutcome(t, log, invocation.ID, outcome))
	}
	duplicate := mustValue((modelCallResult{Response: callResponse(requested("echo", "same"), requested("echo", "same"))}).settlement(run.MaxPayloadBytes))
	assertProjectionRejection(t, program, replaceLoggedOutcome(t, log, model.ID, duplicate))
}
