package interaction

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
					want, err := batch.waitSpec(execution.state.ModelCallCount, execution.state.ToolRound.nextCallIndex())
					if err != nil {
						t.Fatal(err)
					}
					waitID, _ := agent.ParseWaitID("wait:child-batch")
					var payload any
					if stage == phaseAwaitingChildWaitOpen {
						want.Boundary = boundary
						effect, effectErr := agent.NewChildWaitEffect(want)
						if effectErr != nil {
							t.Fatal(effectErr)
						}
						var opening struct {
							Operation string          `json:"operation"`
							Spec      json.RawMessage `json:"spec"`
						}
						if decodeErr := json.Unmarshal(effect.Payload(), &opening); decodeErr != nil {
							t.Fatal(decodeErr)
						}
						opening.Operation = "child_wait_opened"
						payload = opening
					} else {
						output, _ := agent.EncodePayload(fuzzDelegateOutput{Result: "done"})
						if kind == childCallsTool {
							output, _ = agent.EncodePayload(toolCallResult{Result: chat.ToolResult{
								ID: "call_batch", Name: "delegate_fuzz", Output: chat.NewTextToolOutput("done"),
							}})
						}
						payload = childCompletionTestPayload{
							Operation: "child_wait_satisfied", Key: want.Key, Boundary: boundary,
							Outcomes: []childOutcomeTestWire{{
								Boundary: agent.ChildWaitBoundaryDrained, Key: *batch.Invocations[0].ChildKey, SubtreeUnresolvedEffects: []agent.UnresolvedEffect{},
								Result: childResultTestWire{
									ProcessID: want.Children[0], StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0),
									Output: output, Termination: json.RawMessage(`{"status":"completed","cause":"completion"}`),
								},
							}},
						}
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
					} else if execution.state.ToolRound == nil || execution.state.Phase != phaseAwaitingResultCommit {
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
				invalid, err := agent.NewExecutionState(captured.Kind(), []byte(payload))
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
			key, err := execution.state.ToolRound.ChildBatch.childKey(execution.state.ModelCallCount, *call)
			if err != nil {
				t.Fatal(err)
			}
			execution.state.ToolRound.ChildBatch.Invocations[0].ChildKey = &key
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
			MaxModelCalls: 2, MaxConcurrentToolCalls: 3, Tools: tools, ToolBudget: agent.Budget{Steps: 10, Effects: 10, Signals: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	call := chat.ToolCall{ID: "call_batch", Name: "delegate_fuzz", Arguments: `{"task":"check"}`}
	message := chat.NewAssistantMessage(chat.NewToolCallPart(call))
	processID, _ := agent.ParseProcessID("process:child-batch")
	batch := &childCallBatch{Kind: kind, NextStartIndex: 1}
	key, err := batch.childKey(1, call)
	if err != nil {
		t.Fatal(err)
	}
	batch.Invocations = []childInvocationState{{ChildKey: &key, ProcessID: &processID}}
	switch stage {
	case phaseWaitingChildren:
		waitID, _ := agent.ParseWaitID("wait:child-batch")
		batch.WaitID = &waitID
	case phaseAwaitingChildStarts:
		batch.Invocations[0].ProcessID = nil
	}
	state := executionState{
		Phase: stage, ModelCallCount: 1,
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
	Operation string                  `json:"operation"`
	Key       agent.WaitKey           `json:"key"`
	Boundary  agent.ChildWaitBoundary `json:"boundary"`
	Outcomes  []childOutcomeTestWire  `json:"outcomes"`
}

type childOutcomeTestWire struct {
	Boundary                 agent.ChildWaitBoundary  `json:"boundary"`
	Key                      agent.ChildKey           `json:"key"`
	Result                   childResultTestWire      `json:"result"`
	SubtreeUnresolvedEffects []agent.UnresolvedEffect `json:"subtree_unresolved_effects"`
}

type childResultTestWire struct {
	ProcessID   agent.ProcessID `json:"process_id"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  time.Time       `json:"finished_at"`
	Output      agent.Payload   `json:"output,omitzero"`
	Termination json.RawMessage `json:"termination"`
}

func childBatchTestSignal(t testing.TB, waitID agent.WaitID, payload any) agent.Signal {
	t.Helper()
	encoded, err := json.Marshal(struct {
		ID      string       `json:"id"`
		WaitID  agent.WaitID `json:"wait_id"`
		Payload any          `json:"payload"`
	}{ID: "signal:engine:child-batch", WaitID: waitID, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var signal agent.Signal
	if err := json.Unmarshal(encoded, &signal); err != nil {
		t.Fatal(err)
	}
	return signal
}

func TestToolChildTerminationPreservesFailureAndCause(t *testing.T) {
	for _, test := range []struct {
		name, termination, code, message string
		kind                             agent.FailureKind
	}{
		{"host failure", `{"status":"failed","cause":"external_failure","reason":"storage unavailable","failure":{"kind":"external","code":"tool.storage.failed","message":"storage unavailable"}}`, "tool.storage.failed", "storage unavailable", agent.FailureKindExternal},
		{"panic", `{"status":"failed","cause":"panic","reason":"decoder panic","failure":{"kind":"panic","code":"engine.step.panicked","message":"decoder panic"}}`, "engine.step.panicked", "decoder panic", agent.FailureKindPanic},
		{"canceled", `{"status":"canceled","cause":"host_cancellation","reason":"operator stopped job"}`, "interaction.tool.process_failed", "Tool child process:child-batch ended with canceled (host_cancellation): operator stopped job", agent.FailureKindExecution},
		{"deadline", `{"status":"timed_out","cause":"process_deadline","reason":"worker deadline reached"}`, "interaction.tool.process_failed", "Tool child process:child-batch ended with timed_out (process_deadline): worker deadline reached", agent.FailureKindExecution},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution := childBatchTestExecution(t, childCallsTool, phaseWaitingChildren)
			batch := execution.state.ToolRound.ChildBatch
			wait, err := batch.waitSpec(1, 0)
			if err != nil {
				t.Fatal(err)
			}
			signal := childBatchTestSignal(t, *batch.WaitID, childCompletionTestPayload{
				Operation: "child_wait_satisfied", Key: wait.Key, Boundary: agent.ChildWaitBoundaryDrained,
				Outcomes: []childOutcomeTestWire{{Boundary: agent.ChildWaitBoundaryDrained, Key: *batch.Invocations[0].ChildKey, SubtreeUnresolvedEffects: []agent.UnresolvedEffect{}, Result: childResultTestWire{
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
	wait, err := batch.waitSpec(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := make([]childOutcomeTestWire, len(batch.Invocations))
	for index, invocation := range batch.Invocations {
		effectID, _ := agent.ParseEffectID("effect:remote-write")
		outcomes[index] = childOutcomeTestWire{Boundary: agent.ChildWaitBoundaryDrained, Key: *invocation.ChildKey, SubtreeUnresolvedEffects: []agent.UnresolvedEffect{{ProcessID: *invocation.ProcessID, EffectID: effectID}}, Result: childResultTestWire{
			ProcessID: *invocation.ProcessID, StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0),
			Termination: json.RawMessage(`{"status":"killed","cause":"engine_kill","reason":"operator stopped child","unresolved_effect_ids":["effect:remote-write"]}`),
		}}
	}
	signal := childBatchTestSignal(t, *batch.WaitID, childCompletionTestPayload{Operation: "child_wait_satisfied", Key: wait.Key, Boundary: agent.ChildWaitBoundaryDrained, Outcomes: outcomes})
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
				batch.NextStartIndex = 3
				message := chat.NewAssistantMessage()
				outcomes := make([]childOutcomeTestWire, 3)
				for index := range 3 {
					call := chat.ToolCall{ID: fmt.Sprintf("call_%d", index), Name: "delegate_fuzz", Arguments: `{"task":"check"}`}
					message.Parts = append(message.Parts, chat.NewToolCallPart(call))
					key, err := batch.childKey(1, call)
					if err != nil {
						t.Fatal(err)
					}
					id, _ := agent.ParseProcessID(fmt.Sprintf("process:batch-%d", index))
					batch.Invocations = append(batch.Invocations, childInvocationState{ChildKey: &key, ProcessID: &id})
					output, _ := agent.EncodePayload(fuzzDelegateOutput{Result: "done"})
					if kind == childCallsTool {
						output, _ = agent.EncodePayload(toolCallResult{Result: chat.ToolResult{ID: call.ID, Name: call.Name, Output: chat.NewTextToolOutput("done")}})
					}
					outcomes[index] = childOutcomeTestWire{Boundary: agent.ChildWaitBoundaryDrained, Key: key, SubtreeUnresolvedEffects: []agent.UnresolvedEffect{}, Result: childResultTestWire{ProcessID: id, StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0), Output: output, Termination: json.RawMessage(`{"status":"completed","cause":"completion"}`)}}
				}
				execution.state.ToolRound.Response.Output.Message = &message
				if kind == childCallsDelegate {
					effectID, _ := agent.ParseEffectID("effect:remote-write")
					outcomes[failedIndex].SubtreeUnresolvedEffects = []agent.UnresolvedEffect{{ProcessID: outcomes[failedIndex].Result.ProcessID, EffectID: effectID}}
					outcomes[failedIndex].Result.Termination = json.RawMessage(`{"status":"killed","cause":"engine_kill","reason":"stopped","unresolved_effect_ids":["effect:remote-write"]}`)
				} else {
					outcomes[failedIndex].Result.Termination = json.RawMessage(`{"status":"killed","cause":"engine_kill","reason":"stopped"}`)
				}
				outcomes[failedIndex].Result.Output = agent.Payload{}
				before, err := execution.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				if _, restoreErr := execution.definition.Restore(t.Context(), before); restoreErr != nil {
					t.Fatal(restoreErr)
				}
				wait, err := batch.waitSpec(1, 0)
				if err != nil {
					t.Fatal(err)
				}
				signal := childBatchTestSignal(t, *batch.WaitID, childCompletionTestPayload{Operation: "child_wait_satisfied", Key: wait.Key, Boundary: agent.ChildWaitBoundaryDrained, Outcomes: outcomes})
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
