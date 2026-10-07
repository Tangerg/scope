package interaction

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
	"weak"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestChildBatchRequiresDrainedWaitBoundaries(t *testing.T) {
	for _, kind := range []childCallKind{childCallsTool, childCallsDelegate} {
		for _, stage := range []phase{phaseAwaitingChildWaitOpen, phaseWaitingChildren} {
			for _, boundary := range []agent.ChildWaitBoundary{agent.ChildWaitBoundaryResult, agent.ChildWaitBoundaryDrained} {
				t.Run(string(kind)+"/"+string(stage)+"/"+boundary.String(), func(t *testing.T) {
					execution := childBatchTestExecution(t, kind, stage)
					batch := execution.state.ToolRound.ChildBatch
					want, err := batch.waitSpec(execution.state.ModelCallCount, execution.state.ToolRound.nextCallIndex(), mustActiveChildKeys(t, execution))
					if err != nil {
						t.Fatal(err)
					}
					waitID, _ := agent.ParseWaitID("wait:child-batch")
					var payload any
					if stage == phaseAwaitingChildWaitOpen {
						// An opening only acknowledges the wait the batch declared.
						if boundary != agent.ChildWaitBoundaryDrained {
							t.Skip("an opening carries no boundary of its own")
						}
						payload = json.RawMessage(`{"operation":"child_wait_opened"}`)
					} else {
						output, _ := agent.EncodePayload(fuzzDelegateOutput{Result: "done"})
						if kind == childCallsTool {
							output, _ = agent.EncodePayload(toolCallResult{Disposition: ResultSucceeded, Output: chat.NewTextToolOutput("done")})
						}
						outcome := childOutcomeTestWire{Result: childResultTestWire{
							ProcessID: want.Children[0], StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0),
							Termination: completedTermination(t, output),
						}}
						if boundary == agent.ChildWaitBoundaryDrained {
							outcome.DescendantUnresolvedEffects = new([]agent.UnresolvedEffect{})
						}
						payload = childCompletionTestPayload{Operation: "child_wait_satisfied", Outcomes: []childOutcomeTestWire{outcome}}
					}
					signal := childBatchTestSignal(t, waitID, payload)
					transition, err := execution.Step(t.Context(), []agent.Signal{signal})
					if boundary != agent.ChildWaitBoundaryDrained {
						if !errors.Is(err, ErrInvalidExecutionState) {
							t.Fatalf("terminal result crossed the required drain boundary: %v", err)
						}
						return
					}
					if err != nil || transition.ConsumedSignals() != 1 {
						t.Fatalf("drained child transition = %+v, error = %v", transition, err)
					}
					if stage == phaseAwaitingChildWaitOpen {
						if got, waiting := transition.WaitID(); !waiting || got != waitID {
							t.Fatal("wait opening lost the Engine wait identity")
						}
					} else if execution.state.ToolRound == nil || execution.state.phase() != phaseRoundComplete {
						t.Fatal("settled child batch bypassed result publication")
					}
					captured, err := execution.Snapshot()
					if err != nil {
						t.Fatal(err)
					}
					if _, err := execution.definition.Restore(t.Context(), captured); err != nil {
						t.Fatalf("accepted child boundary cannot be restored: %v", err)
					}
				})
			}
		}
	}
}

func TestChildBatchRestoreRejectsUnknownMembers(t *testing.T) {
	for _, kind := range []childCallKind{childCallsTool, childCallsDelegate} {
		execution := childBatchTestExecution(t, kind, phaseWaitingChildren)
		captured, err := execution.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range []string{`"tool_round":{`, `"child_batch":{`, `"invocations":[{`} {
			t.Run(string(kind)+"/"+marker, func(t *testing.T) {
				payload := strings.Replace(string(captured.Payload()), marker, marker+`"unexpected":true,`, 1)
				if payload == string(captured.Payload()) {
					t.Fatal("child batch snapshot lacks its required structure")
				}
				invalid, err := agent.ParseExecutionState(captured.Kind(), []byte(payload))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := execution.definition.Restore(t.Context(), invalid); !errors.Is(err, jsonv2.ErrUnknownName) {
					t.Fatalf("Restore error = %v, want unknown member rejection", err)
				}
			})
		}
	}
}

func TestChildBatchRestoreRequiresDeclaredBinding(t *testing.T) {
	for _, kind := range []childCallKind{childCallsTool, childCallsDelegate} {
		t.Run(string(kind), func(t *testing.T) {
			execution := childBatchTestExecution(t, kind, phaseWaitingChildren)
			call := execution.state.ToolRound.Response.Output.Message.Parts[0].ToolCall
			call.Name = "unavailable"
			captured, err := execution.state.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := execution.definition.Restore(t.Context(), captured); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("Restore admitted an unavailable child binding: %v", err)
			}
		})
	}
}

func TestChildBatchDeclarationSurvivesRecovery(t *testing.T) {
	for _, kind := range []childCallKind{childCallsTool, childCallsDelegate} {
		t.Run(string(kind), func(t *testing.T) {
			current := childBatchTestExecution(t, kind, phaseAwaitingChildStarts)
			for progress := range 2 {
				captured, err := current.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				var wire struct {
					ToolRound struct {
						ChildBatch struct {
							Invocations []map[string]json.RawMessage `json:"invocations"`
						} `json:"child_batch"`
					} `json:"tool_round"`
				}
				if decodeErr := jsonv2.Unmarshal(captured.Payload(), &wire); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				invocations := wire.ToolRound.ChildBatch.Invocations
				if len(invocations) != 1 || invocations[0] == nil || len(invocations[0]) != progress {
					t.Fatalf("admission %d records = %s", progress, captured.Payload())
				}
				if progress == 1 && string(invocations[0]["process_id"]) != `"process:admitted"` {
					t.Fatalf("start receipt lost its identity: %s", captured.Payload())
				}
				restored, err := current.definition.Restore(t.Context(), captured)
				if err != nil {
					t.Fatal(err)
				}
				current = restored.(*execution)
				if progress != 0 {
					continue
				}
				if _, stepErr := current.Step(t.Context(), nil); !errors.Is(stepErr, ErrInvalidExecutionState) {
					t.Fatalf("declared start was reissued after recovery: %v", stepErr)
				}
				encoded, err := jsonv2.Marshal(struct {
					ID      string `json:"id"`
					Payload any    `json:"payload"`
				}{ID: "signal:engine:start", Payload: struct {
					Operation string `json:"operation"`
					ProcessID string `json:"process_id"`
				}{"start_child", "process:admitted"}})
				if err != nil {
					t.Fatal(err)
				}
				var signal agent.Signal
				if decodeErr := jsonv2.Unmarshal(encoded, &signal); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				transition, err := current.Step(t.Context(), []agent.Signal{signal})
				if err != nil || transition.ConsumedSignals() != 1 || len(transition.Effects()) != 1 {
					t.Fatalf("start acknowledgment = %+v, %v", transition, err)
				}
			}
		})
	}
}

func TestToolBatchRestoreRefillsUnscheduledSuffix(t *testing.T) {
	execution := childBatchTestExecution(t, childCallsTool, phaseWaitingChildren)
	batch := execution.state.ToolRound.ChildBatch
	batch.Invocations = make([]*childInvocationState, 5)
	message := chat.NewAssistantMessage()
	for index := range batch.Invocations {
		call := chat.ToolCall{ID: fmt.Sprintf("call_%d", index), Name: "delegate_fuzz", Arguments: `{"task":"check"}`}
		message.Parts = append(message.Parts, chat.NewToolCallPart(call))
		if index >= 4 {
			continue
		}
		id, _ := agent.ParseProcessID(fmt.Sprintf("process:batch-%d", index))
		batch.Invocations[index] = &childInvocationState{ProcessID: &id}
	}
	batch.Invocations[0].Result = &toolCallResult{Disposition: ResultSucceeded, Output: chat.NewTextToolOutput("settled prefix")}
	execution.state.ToolRound.Response.Output.Message = &message
	captured, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := execution.definition.Restore(t.Context(), captured)
	if err != nil {
		t.Fatal(err)
	}
	output, err := agent.EncodePayload(toolCallResult{Disposition: ResultSucceeded, Output: chat.NewTextToolOutput("newly settled")})
	if err != nil {
		t.Fatal(err)
	}
	signal := childBatchTestSignal(t, *batch.WaitID, childCompletionTestPayload{
		Operation: "child_wait_satisfied",
		Outcomes: []childOutcomeTestWire{{
			DescendantUnresolvedEffects: new([]agent.UnresolvedEffect{}),
			Result: childResultTestWire{
				ProcessID: *batch.Invocations[1].ProcessID, StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0),
				Termination: completedTermination(t, output),
			},
		}},
	})
	transition, err := restored.Step(t.Context(), []agent.Signal{signal})
	if err != nil {
		t.Fatal(err)
	}
	if transition.ConsumedSignals() != 1 || len(transition.Effects()) != 1 {
		t.Fatalf("restored batch did not refill its suffix: %+v", transition)
	}
	for _, effect := range transition.Effects() {
		var start struct {
			Spec agent.ChildSpec `json:"spec"`
		}
		if err = jsonv2.Unmarshal(effect.Payload(), &start); err != nil {
			t.Fatal(err)
		}
		call, decodeErr := start.Spec.Input.Decode[toolCall]()
		if decodeErr != nil || call.ModelCallSequence != 1 || call.ToolCallIndex != 4 || call.Call.ID != "call_4" {
			t.Fatalf("refilled call = %+v, error = %v", call, decodeErr)
		}
	}
	after, err := restored.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.definition.Restore(t.Context(), after); err != nil {
		t.Fatalf("refilled batch cannot restore: %v", err)
	}
}

func TestChildBatchRestoreRejectsAdmissionGaps(t *testing.T) {
	for _, kind := range []childCallKind{childCallsTool, childCallsDelegate} {
		t.Run(string(kind), func(t *testing.T) {
			execution := childBatchTestExecution(t, kind, phaseAwaitingChildStarts)
			batch := execution.state.ToolRound.ChildBatch
			for index := 1; index < 3; index++ {
				call := chat.ToolCall{ID: fmt.Sprintf("call_%d", index), Name: "delegate_fuzz", Arguments: `{"task":"check"}`}
				execution.state.ToolRound.Response.Output.Message.Parts = append(execution.state.ToolRound.Response.Output.Message.Parts, chat.NewToolCallPart(call))
				var invocation *childInvocationState
				if index == 2 {
					invocation = &childInvocationState{}
				}
				batch.Invocations = append(batch.Invocations, invocation)
			}
			captured, err := execution.state.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := execution.definition.Restore(t.Context(), captured); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("Restore admitted a gap in child scheduling: %v", err)
			}
		})
	}
}

func childBatchTestExecution(t testing.TB, kind childCallKind, stage phase) *execution {
	t.Helper()
	definition := fuzzInteractionDefinition(t)
	if kind == childCallsTool {
		executable, err := tool.NewFunc(tool.FuncConfig{
			Name: "delegate_fuzz", Description: "Exercise child call protocol boundaries.",
		}, func(context.Context, fuzzDelegateInput) (string, error) { return "done", nil })
		if err != nil {
			t.Fatal(err)
		}
		tools, err := NewToolSet(ToolSetConfig{
			Name: "interaction.child_batch.tools", Description: "Exercise the child call protocol.",
			Tools:                []tool.Tool{batchConcurrentTool{executable}},
			ImplementationDigest: agent.ComputeDigest([]byte("child-batch-tool")),
			ConfigurationDigest:  agent.ComputeDigest([]byte("child-batch-config")),
		})
		if err != nil {
			t.Fatal(err)
		}
		definition, err = NewDefinition(DefinitionConfig{
			Name: "interaction.child_batch", Description: "Exercise the child call protocol.",
			MaxModelCalls: agent.NewQuota(2), MaxConcurrentToolCalls: 3, Tools: tools, ToolBudget: agent.Budget{Steps: agent.NewQuota(10), Effects: agent.NewQuota(10), Signals: agent.NewQuota(10)},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	call := chat.ToolCall{ID: "call_batch", Name: "delegate_fuzz", Arguments: `{"task":"check"}`}
	message := chat.NewAssistantMessage(chat.NewToolCallPart(call))
	processID, _ := agent.ParseProcessID("process:child-batch")
	batch := &childCallBatch{Kind: kind}
	batch.Invocations = []*childInvocationState{{ProcessID: &processID}}
	switch stage {
	case phaseWaitingChildren:
		waitID, _ := agent.ParseWaitID("wait:child-batch")
		batch.WaitID = &waitID
	case phaseAwaitingChildStarts:
		batch.Invocations[0].ProcessID = nil
	}
	state := executionState{
		ModelCallCount: 1,
		WorkingContext: &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}},
		ToolRound: &toolCallRound{Response: &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}},
			ChildBatch: batch},
	}
	captured, err := state.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := definition.Restore(t.Context(), captured)
	if err != nil {
		t.Fatal(err)
	}
	return restored.(*execution)
}

type childCompletionTestPayload struct {
	Operation string                 `json:"operation"`
	Outcomes  []childOutcomeTestWire `json:"outcomes"`
}

// childOutcomeTestWire carries DescendantUnresolvedEffects exactly when the child
// drained.
type childOutcomeTestWire struct {
	Result                      childResultTestWire       `json:"result"`
	DescendantUnresolvedEffects *[]agent.UnresolvedEffect `json:"descendant_unresolved_effects,omitzero"`
}

type childResultTestWire struct {
	ProcessID   agent.ProcessID `json:"process_id"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  time.Time       `json:"finished_at"`
	Termination json.RawMessage `json:"termination"`
	Usage       agent.Usage     `json:"usage"`
}

func completedTermination(t testing.TB, output agent.Payload) json.RawMessage {
	t.Helper()
	data, err := jsonv2.Marshal(struct {
		Output agent.Payload `json:"output"`
	}{output})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func childBatchTestSignal(t testing.TB, waitID agent.WaitID, payload any) agent.Signal {
	t.Helper()
	encoded, err := jsonv2.Marshal(struct {
		ID      string       `json:"id"`
		WaitID  agent.WaitID `json:"wait_id"`
		Payload any          `json:"payload"`
	}{ID: "signal:engine:child-batch", WaitID: waitID, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var signal agent.Signal
	if err := jsonv2.Unmarshal(encoded, &signal); err != nil {
		t.Fatal(err)
	}
	return signal
}

func TestToolChildTerminationPreservesFailureAndCause(t *testing.T) {
	for _, test := range []struct {
		name, termination, code, message string
		kind                             agent.FailureKind
	}{
		{"host failure", `{"failure":{"kind":"external","code":"tool.storage.failed","message":"storage unavailable"}}`, "tool.storage.failed", "storage unavailable", agent.FailureKindExternal},
		{"panic", `{"failure":{"kind":"panic","code":"engine.step.panicked","message":"decoder panic"}}`, "engine.step.panicked", "decoder panic", agent.FailureKindPanic},
		{"canceled", `{"cause":"host_cancellation","reason":"operator stopped job"}`, "interaction.tool.process_failed", "Tool child process:child-batch ended with canceled (host_cancellation): operator stopped job", agent.FailureKindExecution},
		{"deadline", `{"cause":"host_deadline","reason":"worker deadline reached"}`, "interaction.tool.process_failed", "Tool child process:child-batch ended with timed_out (host_deadline): worker deadline reached", agent.FailureKindExecution},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution := childBatchTestExecution(t, childCallsTool, phaseWaitingChildren)
			batch := execution.state.ToolRound.ChildBatch
			signal := childBatchTestSignal(t, *batch.WaitID, childCompletionTestPayload{
				Operation: "child_wait_satisfied",
				Outcomes: []childOutcomeTestWire{{DescendantUnresolvedEffects: new([]agent.UnresolvedEffect{}), Result: childResultTestWire{
					ProcessID: *batch.Invocations[0].ProcessID, StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0), Termination: json.RawMessage(test.termination),
				}}},
			})
			transition, err := execution.Step(t.Context(), []agent.Signal{signal})
			if err != nil {
				t.Fatal(err)
			}
			failure, failed := transition.Failure()
			if !failed || failure.Kind() != test.kind || failure.Code() != test.code || failure.Message() != test.message {
				t.Fatalf("child diagnostic was replaced: failure = %+v", failure)
			}
		})
	}
}

func TestDelegateUnresolvedEffectsStopParent(t *testing.T) {
	execution := childBatchTestExecution(t, childCallsDelegate, phaseWaitingChildren)
	batch := execution.state.ToolRound.ChildBatch
	outcomes := make([]childOutcomeTestWire, len(batch.Invocations))
	for index, invocation := range batch.Invocations {
		outcomes[index] = childOutcomeTestWire{DescendantUnresolvedEffects: new([]agent.UnresolvedEffect{}), Result: childResultTestWire{
			ProcessID: *invocation.ProcessID, StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0),
			Termination: json.RawMessage(`{"cause":"engine_kill","reason":"operator stopped child","unresolved_effect_ids":["effect:remote-write"]}`),
		}}
	}
	signal := childBatchTestSignal(t, *batch.WaitID, childCompletionTestPayload{Operation: "child_wait_satisfied", Outcomes: outcomes})
	transition, err := execution.Step(t.Context(), []agent.Signal{signal})
	if err != nil {
		t.Fatal(err)
	}
	failure, failed := transition.Failure()
	if !failed || failure.Code() != "interaction.delegate.unresolved_effects" || !strings.Contains(failure.Message(), "effect:remote-write") || len(transition.Effects()) != 0 {
		t.Fatalf("unresolved Delegate continued: %+v", transition)
	}
}

func TestBatchFailureAfterSuccessPrefixRemainsRestorable(t *testing.T) {
	for _, kind := range []childCallKind{childCallsDelegate, childCallsTool} {
		for failedIndex := range 3 {
			t.Run(fmt.Sprintf("%s/failure_%d", kind, failedIndex), func(t *testing.T) {
				execution := childBatchTestExecution(t, kind, phaseWaitingChildren)
				batch := execution.state.ToolRound.ChildBatch
				batch.Invocations = nil
				message := chat.NewAssistantMessage()
				outcomes := make([]childOutcomeTestWire, 3)
				for index := range 3 {
					call := chat.ToolCall{ID: fmt.Sprintf("call_%d", index), Name: "delegate_fuzz", Arguments: `{"task":"check"}`}
					message.Parts = append(message.Parts, chat.NewToolCallPart(call))
					id, _ := agent.ParseProcessID(fmt.Sprintf("process:batch-%d", index))
					batch.Invocations = append(batch.Invocations, &childInvocationState{ProcessID: &id})
					output, _ := agent.EncodePayload(fuzzDelegateOutput{Result: "done"})
					if kind == childCallsTool {
						output, _ = agent.EncodePayload(toolCallResult{Disposition: ResultSucceeded, Output: chat.NewTextToolOutput("done")})
					}
					outcomes[index] = childOutcomeTestWire{DescendantUnresolvedEffects: new([]agent.UnresolvedEffect{}), Result: childResultTestWire{ProcessID: id, StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0), Termination: completedTermination(t, output)}}
				}
				execution.state.ToolRound.Response.Output.Message = &message
				if kind == childCallsDelegate {
					outcomes[failedIndex].Result.Termination = json.RawMessage(`{"cause":"engine_kill","reason":"stopped","unresolved_effect_ids":["effect:remote-write"]}`)
				} else {
					outcomes[failedIndex].Result.Termination = json.RawMessage(`{"cause":"engine_kill","reason":"stopped"}`)
				}
				before, err := execution.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				if _, restoreErr := execution.definition.Restore(t.Context(), before); restoreErr != nil {
					t.Fatal(restoreErr)
				}
				signal := childBatchTestSignal(t, *batch.WaitID, childCompletionTestPayload{Operation: "child_wait_satisfied", Outcomes: outcomes})
				transition, err := execution.Step(t.Context(), []agent.Signal{signal})
				if err != nil {
					t.Fatal(err)
				}
				wantCode := "interaction.delegate.unresolved_effects"
				if kind == childCallsTool {
					wantCode = "interaction.tool.process_failed"
				}
				failure, failed := transition.Failure()
				if !failed || failure.Code() != wantCode || len(transition.Effects()) != 0 {
					t.Fatalf("failure = %+v", transition)
				}
				after, err := execution.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := execution.definition.Restore(t.Context(), after); err != nil {
					t.Fatalf("failure candidate cannot restore: %v", err)
				}
				if !bytes.Equal(before.Payload(), after.Payload()) {
					t.Fatal("failure installed a partial batch")
				}
			})
		}
	}
}

type batchConcurrentTool struct{ tool.Tool }

func (b batchConcurrentTool) ConcurrencyPolicy() func(tool.Invocation) (string, bool) {
	return func(tool.Invocation) (string, bool) { return "", true }
}

type panickingClassifierTool struct{ tool.Tool }

func (panickingClassifierTool) ConcurrencyPolicy() func(tool.Invocation) (string, bool) {
	return func(tool.Invocation) (string, bool) { panic("classifier failed") }
}

// retainedTool is addressable so a weak pointer can observe whether the
// scheduling manifest keeps it alive.
type retainedTool struct{ tool.Tool }

func (*retainedTool) ConcurrencyPolicy() func(tool.Invocation) (string, bool) {
	return func(tool.Invocation) (string, bool) { return "stable", true }
}

func TestToolManifestDoesNotRetainExecutableTools(t *testing.T) {
	manifest, executable := isolatedToolManifest(t)
	runtime.GC()
	runtime.GC()
	if executable.Value() != nil {
		t.Fatal("scheduling manifest retained the executable Tool")
	}
	calls := []chat.ToolCall{{ID: "call", Name: "retained", Arguments: `{"task":"run"}`}}
	if end, err := manifest.concurrentBatchEnd(t.Context(), calls); err != nil || end != 1 {
		t.Fatalf("frozen classifier = %d, %v", end, err)
	}
}

func isolatedToolManifest(t *testing.T) (toolManifest, weak.Pointer[retainedTool]) {
	t.Helper()
	executable, err := tool.NewFunc(tool.FuncConfig{
		Name: "retained", Description: "Exercise scheduling isolation.",
	}, func(context.Context, fuzzDelegateInput) (string, error) { return "done", nil })
	if err != nil {
		t.Fatal(err)
	}
	retained := &retainedTool{executable}
	tools, err := NewToolSet(ToolSetConfig{
		Name: "interaction.retention.tools", Description: "Exercise scheduling isolation.",
		Tools:                []tool.Tool{retained},
		ImplementationDigest: agent.ComputeDigest([]byte("retention-tool")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("retention-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tools.manifest(), weak.Make(retained)
}

func TestConcurrencyClassifierPanicFailsScheduling(t *testing.T) {
	executable, err := tool.NewFunc(tool.FuncConfig{
		Name: "classified", Description: "Exercise classifier panic isolation.",
	}, func(context.Context, fuzzDelegateInput) (string, error) { return "done", nil })
	if err != nil {
		t.Fatal(err)
	}
	tools, err := NewToolSet(ToolSetConfig{
		Name: "interaction.classifier.tools", Description: "Exercise classifier panic isolation.",
		Tools:                []tool.Tool{panickingClassifierTool{executable}},
		ImplementationDigest: agent.ComputeDigest([]byte("classifier-tool")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("classifier-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := []chat.ToolCall{{ID: "call", Name: "classified", Arguments: `{"task":"run"}`}}
	end, err := tools.manifest().concurrentBatchEnd(t.Context(), calls)
	panicErr, isPanic := errors.AsType[*agent.CallbackPanicError](err)
	if end != 0 || !isPanic || panicErr.Operation != "ConcurrentTool policy" || panicErr.Value != "classifier failed" {
		t.Fatalf("classifier panic = %d, %v", end, err)
	}
}

func mustActiveChildKeys(t testing.TB, execution *execution) []agent.ChildKey {
	t.Helper()
	keys, err := execution.activeChildKeys(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return keys
}
