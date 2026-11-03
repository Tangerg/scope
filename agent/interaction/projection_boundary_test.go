package interaction

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

func TestPublicIntentDecodersPreserveVariantAndDetachedRequest(t *testing.T) {
	model := mustValue(newModelEffect(new(testInput()), 1, []tool.Definition{scriptTool{name: "echo"}.Definition()}, nil))
	modelEffect := mustValue(run.NewDispatcherEffect(mustValue(jsonv2.Marshal(model))))
	request, recognized, err := ModelRequestFromEffect(modelEffect)
	if err != nil || !recognized || len(request.Tools) != 1 || request.Tools[0].Name != "echo" {
		t.Fatalf("model intent=%+v recognized=%t error=%v", request, recognized, err)
	}
	request.Messages[0].Parts[0].Text = "mutated"
	request.Tools[0].InputSchema[0] = '['
	retained, _, decodeErr := ModelRequestFromEffect(modelEffect)
	if decodeErr != nil || retained.Messages[0].Parts[0].Text != "work" {
		t.Fatal("model projection changed the persisted request", decodeErr)
	}
	retainedSchema := mustValue(jsonschema.Parse(retained.Tools[0].InputSchema))
	if !retainedSchema.Equal(mustValue(jsonschema.Parse(scriptTool{name: "echo"}.Definition().InputSchema))) {
		t.Fatal("model projection changed the persisted tool contract")
	}
	if _, _, toolCall, decodeErr := ToolCallFromEffect(modelEffect); decodeErr != nil || toolCall {
		t.Fatal("model intent became a tool call", decodeErr)
	}
	call := requested("echo", "one")
	question := mustValue(newInputRequest([]byte(`"Approve?"`), []byte(`{"type":"boolean"}`), []byte(`{"private":true}`)))
	reference := testTurn(1).EffectID(1)
	for _, resume := range []*toolResume{nil, {InputRequest: question, Reference: reference, InputResponse: []byte(`true`)}} {
		envelope := mustValue(newToolEffect(toolDispatchRequest{Invocation: toolCall{Call: call}, Resume: resume}))
		effect := mustValue(run.NewDispatcherEffect(mustValue(jsonv2.Marshal(envelope))))
		observed, origin, present, decodeErr := ToolCallFromEffect(effect)
		if decodeErr != nil || !present || observed != call || (resume != nil) != origin.Valid() || resume != nil && origin != reference {
			t.Fatal("tool intent lost its exact call or continuation", observed, origin, decodeErr)
		}
		if _, isModel, decodeErr := ModelRequestFromEffect(effect); decodeErr != nil || isModel {
			t.Fatal("tool intent became a model request", decodeErr)
		}
	}
	child := mustValue(testTurn(1).Process.Child(mustValue(run.ParseChildKey("child"))))
	preparation := mustValue(run.NewDispatcherEffect(mustValue(jsonv2.Marshal(effectEnvelope{Preparation: model.ModelCall}))))
	for _, effect := range []run.Effect{{}, mustValue(run.NewChildCancelEffect(child, "finished")), preparation} {
		if value, found, decodeErr := ModelRequestFromEffect(effect); decodeErr != nil || found || value != nil {
			t.Fatal("kernel effect acquired model semantics", decodeErr)
		}
		if _, _, found, decodeErr := ToolCallFromEffect(effect); decodeErr != nil || found {
			t.Fatal("kernel effect acquired tool semantics", decodeErr)
		}
	}
	for _, raw := range []string{`null`, `{}`, `{"model_call":{}}`, `{"tool_call":{}}`, `{"model_call":{},"tool_call":{}}`} {
		effect := mustValue(run.NewDispatcherEffect([]byte(raw)))
		if _, _, decodeErr := ModelRequestFromEffect(effect); decodeErr == nil {
			t.Fatal("malformed model protocol admitted", raw)
		}
		if _, _, _, decodeErr := ToolCallFromEffect(effect); decodeErr == nil {
			t.Fatal("malformed tool protocol admitted", raw)
		}
	}
}

func TestPublicSettlementDecodersRequireTheMatchingEvidenceVariant(t *testing.T) {
	call := requested("echo", "one")
	model := mustValue((modelCallResult{Response: finalResponse("done")}).settlement(run.MaxPayloadBytes))
	toolResult := mustValue((toolDispatchResult{Completion: &toolCallResult{Disposition: ResultSucceeded, Output: tool.NewTextOutput("echo")}}).settlement())
	failure := mustValue(run.NewFailure(run.FailureKindExternal, failureCodeModelFailed, "rejected"))
	nullDetails := mustValue(run.NewFailedSettlement(failure, mustValue(run.ParsePayload([]byte(`null`)))))
	for _, outcome := range []run.Settlement{{}, toolResult, mustValue(run.NewSettlement(run.SettlementStatusSucceeded, []byte(`{}`))), nullDetails} {
		if _, err := ModelSettlementFromOutcome(outcome); err == nil {
			t.Fatal("model decoder admitted another or missing outcome")
		}
	}
	for _, outcome := range []run.Settlement{{}, model, mustValue(run.NewSettlement(run.SettlementStatusSucceeded, []byte(`{}`))), nullDetails} {
		if _, err := ToolSettlementFromOutcome(call, outcome); err == nil {
			t.Fatal("tool decoder admitted another or missing outcome")
		}
	}
	if _, err := ToolSettlementFromOutcome(chat.ToolCall{}, toolResult); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("tool result fabricated call identity", err)
	}
	for _, sample := range []struct {
		status run.SettlementStatus
		wire   string
	}{
		{run.SettlementStatusFailed, `{"diagnostic":" rejected "}`},
		{run.SettlementStatusFailed, `{"model":{"diagnostic":"rejected","not_executed":true,"response":{"output":{"finish_reason":"stop"}}}}`},
		{run.SettlementStatusUnknown, `{"model":{"diagnostic":"lost","response":{}}}`},
		{run.SettlementStatusUnknown, `{"tool":{"diagnostic":" "}}`},
		{run.SettlementStatusUnknown, `{"tool":{"diagnostic":"lost","evidence":{"content":[{"kind":"unknown"}]}}}`},
	} {
		var outcome run.Settlement
		if sample.status == run.SettlementStatusFailed {
			outcome = mustValue(run.NewFailedSettlement(failure, mustValue(run.ParsePayload([]byte(sample.wire)))))
		} else {
			outcome = mustValue(run.NewSettlement(sample.status, []byte(sample.wire)))
		}
		if _, err := ModelSettlementFromOutcome(outcome); err == nil {
			t.Fatal("invalid failure became model evidence", sample.wire)
		}
		if _, err := ToolSettlementFromOutcome(call, outcome); err == nil {
			t.Fatal("invalid failure became tool evidence", sample.wire)
		}
	}
	failed := mustValue(failedSettlement(errors.New("local admission rejected")))
	observedFailure := mustValue(ToolSettlementFromOutcome(call, failed))
	if observedFailure.Failure != "local admission rejected" || observedFailure.Result != nil || observedFailure.Evidence != nil {
		t.Fatal("definite local rejection acquired external evidence")
	}
	uncertain := mustValue((dispatchUncertainty{Tool: &toolUncertainty{Diagnostic: "acknowledgment lost", Evidence: new(tool.NewTextOutput("unconfirmed output"))}}).settlement())
	evidence := mustValue(ToolSettlementFromOutcome(call, uncertain))
	evidence.Evidence.Content[0].Text = "changed"
	retained := mustValue(ToolSettlementFromOutcome(call, uncertain))
	if retained.Evidence.Content[0].Text != "unconfirmed output" || retained.Result != nil || retained.InputRequired {
		t.Fatal("unknown evidence became final or was mutated")
	}
	emptyEvidence := mustValue((dispatchUncertainty{Tool: &toolUncertainty{Diagnostic: "acknowledgment lost", Evidence: new(tool.Output)}}).settlement())
	empty := mustValue(ToolSettlementFromOutcome(call, emptyEvidence))
	if empty.Evidence == nil || empty.Evidence.Validate() != nil || empty.Result != nil {
		t.Fatal("present empty evidence was treated as absent or final")
	}
	question := mustValue(newInputRequest([]byte(`"Approve?"`), []byte(`{"type":"boolean"}`), []byte(`null`)))
	input := mustValue(ToolSettlementFromOutcome(call, mustValue((toolDispatchResult{InputRequest: &question}).settlement())))
	if !input.InputRequired || input.Result != nil || input.Failure != "" {
		t.Fatal("input request became a final tool result")
	}
	for _, disposition := range []ResultDisposition{ResultSucceeded, ResultFailed, ResultRejected} {
		outcome := mustValue((toolDispatchResult{Completion: &toolCallResult{Disposition: disposition, Output: tool.NewTextOutput("result")}}).settlement())
		observed := mustValue(ToolSettlementFromOutcome(call, outcome))
		if observed.Result == nil || observed.Result.ID != call.ID || observed.Result.Name != call.Name || observed.Result.IsError != (disposition != ResultSucceeded) {
			t.Fatal("model-visible result changed its call or disposition")
		}
		if !disposition.Valid() || disposition.String() != string(disposition) {
			t.Fatal("result vocabulary changed meaning")
		}
	}
	if ResultInvalid.Valid() || ResultInvalid.String() != invalidEnumName || ModelOutcomeInvalid.Valid() || !ModelOutcomeUnknown.Valid() {
		t.Fatal("invalid enum acquired valid evidence")
	}
}

func TestPendingInputBoundaryDoesNotExposeContinuationState(t *testing.T) {
	key := mustValue(run.ParseWaitKey("question"))
	schema := mustValue(jsonschema.For[bool]())
	pending := PendingToolInput{processID: testTurn(1).Process, waitKey: key, prompt: []byte(`"Approve?"`), responseSchema: schema}
	for _, value := range []PendingToolInput{{}, {processID: pending.processID, prompt: pending.prompt, responseSchema: schema}, {processID: pending.processID, waitKey: key}} {
		if value.Valid() {
			t.Fatal("incomplete question acquired an answer authority")
		}
		if _, err := value.ResponseSignal(mustValue(run.ParseSignalID("answer")), []byte(`true`)); !errors.Is(err, ErrInvalidPendingToolInput) {
			t.Fatal("invalid question accepted an answer", err)
		}
	}
	if _, err := pending.ResponseSignal(run.SignalID{}, []byte(`true`)); !errors.Is(err, ErrInvalidPendingToolInput) {
		t.Fatal("answer without identity admitted", err)
	}
	for _, response := range []json.RawMessage{nil, []byte(`{`), []byte(`"not a boolean"`), bytes.Repeat([]byte(" "), tool.MaxInputBytes+1)} {
		if _, err := pending.ResponseSignal(mustValue(run.ParseSignalID("answer")), response); err == nil {
			t.Fatal("malformed or oversized answer admitted")
		}
	}
	prompt, contract := pending.Prompt(), pending.ResponseSchema()
	prompt[0], contract[0] = '[', '['
	if string(pending.Prompt()) != `"Approve?"` || !bytes.Equal(pending.ResponseSchema(), schema.JSON()) {
		t.Fatal("question projection aliases its caller")
	}
	answer := mustValue(pending.ResponseSignal(mustValue(run.ParseSignalID("answer")), []byte(` true `)))
	if !answer.Valid() || answer.Wait != key || string(answer.Payload.JSON()) != "true" {
		t.Fatal("answer changed its exact address or normalized value")
	}
}

func TestSparseRoundsRetainProviderOrderAndOnlyCommittedReferences(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	executable := scriptTool{name: "echo", direct: true, invoke: func(_ context.Context, input tool.Invocation) (tool.Result, error) {
		if requestedCall(input) == "uncertain" {
			return tool.Result{}, mustValue(tool.NewCallError(tool.CallErrorConfig{Cause: errors.New("lost confirmation"), Evidence: tool.NewTextOutput("uncertain output")}))
		}
		return tool.Result{Output: tool.NewTextOutput("first output")}, nil
	}}
	program := compileTest(t, runtime, Config{Tools: toolSetTest(t, executable), Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("echo", "first"), requested("echo", "uncertain")), nil
	})})
	handle := startInteraction(t, runtime, program)
	log := awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		for _, entry := range log.Entries {
			if result, ok := entry.Record.(run.EffectSettled); ok && result.Outcome.Status() == run.SettlementStatusUnknown {
				return true
			}
		}
		return false
	})
	rounds := mustValue(SettledResults(program, log))
	if len(rounds) != 1 || rounds[0].Complete() || rounds[0].CallCount() != 2 || len(rounds[0].Entries()) != 1 || rounds[0].Entries()[0].ToolCallIndex != 0 {
		t.Fatal("missing outcome changed provider indexes or fabricated completion")
	}
	if _, found := rounds[0].Reference(1); !found {
		t.Fatal("unknown outcome lost its committed intent")
	}
	if _, found := rounds[0].Reference(2); found {
		t.Fatal("out-of-range proposal acquired a reference")
	}
	entries := rounds[0].Entries()
	entries[0].Result.Output.Content[0].Text = "changed"
	if rounds[0].Entries()[0].Result.Output.Content[0].Text != "first output" {
		t.Fatal("detached result mutated a round projection")
	}
	if inputs := PendingToolInputs(program, mustValue(log.Inspect())); len(inputs) != 0 {
		t.Fatal("unknown effect acquired an input question")
	}
	inspection := mustValue(log.Inspect())
	process, _ := inspection.Process(handle.ID())
	if len(process.PendingEffects) != 1 {
		t.Fatal("unknown effect missing")
	}
	request := process.PendingEffects[0].Request
	settlement := mustValue(SettleToolResult(program, request, tool.Result{Output: tool.NewTextOutput("confirmed")}, ResultSucceeded, nil))
	if err := handle.ResolveUnknownEffect(t.Context(), request.ID(), settlement); err != nil {
		t.Fatal(err)
	}
	awaitOutput(t, handle)
	completed := mustValue(SettledResults(program, completedLog(t, store, handle.ID())))
	if len(completed) != 1 || !completed[0].Complete() || completed[0].Entries()[1].Call.ID != "uncertain" || completed[0].Entries()[1].Reference != request.ID() {
		t.Fatal("resolution created a second logical call or changed provider order")
	}
}

func TestLogProjectionsFilterExactProgramAndRejectInvalidEvidence(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return finalResponse("done"), nil })
	program := compileTest(t, runtime, Config{Model: model})
	handle := startInteraction(t, runtime, program)
	awaitOutput(t, handle)
	log := completedLog(t, store, handle.ID())
	different := mustValue(Compile(runtime.Compiler(), Config{Name: program.Descriptor().Name(), Description: program.Descriptor().Description(), Revision: "another-configuration", Model: model}))
	if got := mustValue(SettledResults(different, log)); len(got) != 0 {
		t.Fatal("program name overrode exact binding identity")
	}
	if got := PendingToolInputs(different, mustValue(log.Inspect())); len(got) != 0 {
		t.Fatal("foreign program acquired an input question")
	}
	if got := mustValue(DelegateAttributions(different, log)); len(got) != 0 {
		t.Fatal("foreign program acquired a delegated child")
	}
	stage := mustValue(workflow.Transform("finish", func(context.Context, Input) (Output, error) {
		return Output{ModelCalls: 1, ModelResponse: finalResponse("done")}, nil
	}))
	otherStrategy := mustValue(workflow.Compile[Input, Output](runtime.Compiler(), workflow.Config{Name: "test.workflow", Description: "Another strategy with identical Go shapes.", Revision: "finish", Stages: []workflow.Stage{stage}}))
	for _, invalid := range []run.Program[Input, Output]{{}, otherStrategy} {
		assertProjectionRejection(t, invalid, log)
	}
	broken := mustValue(log.Clone())
	broken.Entries[2].LSN++
	assertProjectionRejection(t, program, broken)
	wrongVariant := mustValue((toolDispatchResult{Completion: &toolCallResult{Disposition: ResultSucceeded, Output: tool.NewTextOutput("wrong result")}}).settlement())
	for _, raw := range []json.RawMessage{[]byte(`{}`), wrongVariant.Payload().JSON()} {
		broken := mustValue(log.Clone())
		for index, entry := range broken.Entries {
			if result, ok := entry.Record.(run.EffectSettled); ok && result.Outcome.Status() == run.SettlementStatusSucceeded {
				result.Outcome = mustValue(run.NewSettlement(run.SettlementStatusSucceeded, raw))
				broken.Entries[index].Record = result
				break
			}
		}
		assertProjectionRejection(t, program, broken)
	}
}

func assertProjectionRejection(t *testing.T, program run.Program[Input, Output], log run.Log) {
	t.Helper()
	if _, err := SettledResults(program, log); err == nil {
		t.Fatal("invalid log or program acquired settled results")
	}
	if _, err := DelegateAttributions(program, log); err == nil {
		t.Fatal("invalid log or program acquired delegate attribution")
	}
}

func TestDelegateProjectionDistinguishesAdmissionRejectionAndChildFailure(t *testing.T) {
	for _, rejected := range []bool{true, false} {
		name := "failed child"
		if rejected {
			name = "rejected admission"
		}
		t.Run(name, func(t *testing.T) {
			store := run.NewMemoryStore()
			runtime := conformancetest.Runtime(t, run.Config{Store: store, ProcessAdmitter: run.ProcessAdmitterFunc(func(_ context.Context, admission run.ProcessAdmission) error {
				if rejected && admission.ProcessID().Depth() > 0 {
					return errors.New("delegate admission rejected")
				}
				return nil
			})})
			delegate := compiledDelegate(t, runtime.Compiler(), "worker", func(context.Context, delegateValue) (delegateValue, error) {
				return delegateValue{}, errors.New("delegate execution failed")
			})
			calls := 0
			program := compileTest(t, runtime, Config{Delegates: []Delegate{delegate}, Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				calls++
				if calls == 1 {
					return callResponse(chat.ToolCall{ID: "delegation", Name: "worker", Arguments: `{"value":3}`}), nil
				}
				return finalResponse("reported failure"), nil
			})})
			handle := startInteraction(t, runtime, program)
			awaitOutput(t, handle)
			log := completedLog(t, store, handle.ID())
			rounds := mustValue(SettledResults(program, log))
			if len(rounds) != 1 || !rounds[0].Complete() || len(rounds[0].Entries()) != 1 || !rounds[0].Entries()[0].Result.IsError {
				t.Fatal("unsuccessful delegate became a successful result")
			}
			entry := rounds[0].Entries()[0]
			attribution := mustValue(DelegateAttributions(program, log))
			if rejected {
				if entry.Disposition != ResultRejected || len(attribution) != 0 {
					t.Fatal("unstarted delegate acquired a child lifecycle")
				}
			} else if entry.Disposition != ResultFailed || len(attribution) != 1 || !attribution[0].Valid() {
				t.Fatal("failed child lost its admitted lifecycle")
			}
			if !entry.Reference.Valid() {
				t.Fatal("failed spawn lost its committed effect identity")
			}
		})
	}
	var missing DelegateAttribution
	if missing.Valid() {
		t.Fatal("empty attribution invented an admitted child")
	}
	if _, found := missing.Reference(); found {
		t.Fatal("empty attribution invented a spawn reference")
	}
}
