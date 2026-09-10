package interaction

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
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
						effect, effectErr := agent.WaitForChildren(want)
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
						output, _ := agent.EncodeOutput(fuzzDelegateOutput{Result: "done"})
						if kind == childCallsTool {
							output, _ = agent.EncodeOutput(toolCallResult{Result: &chat.ToolResult{
								ID: "call_batch", Name: "delegate_fuzz", Output: chat.NewTextToolOutput("done"),
							}})
						}
						payload = childCompletionTestPayload{
							Operation: "child_wait_satisfied", Key: want.Key, Boundary: boundary,
							Outcomes: []childOutcomeTestWire{{
								Key: *batch.Invocations[0].ChildKey,
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
					} else if execution.state.ToolRound != nil || execution.state.Phase != phaseAwaitingModel {
						t.Fatal("settled child batch did not continue the model loop")
					}
					captured, err := execution.Snapshot()
					if err != nil {
						t.Fatal(err)
					}
					if _, err := execution.definition.Restore(captured); err != nil {
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
				if _, err := execution.definition.Restore(invalid); !errors.Is(err, jsonv2.ErrUnknownName) {
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
			captured, err := encodeState(execution.state)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := execution.definition.Restore(captured); !errors.Is(err, ErrInvalidExecutionState) {
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
			Tools:                []tool.Tool{executable},
			ImplementationDigest: agent.ComputeDigest([]byte("child-batch-tool")),
			ConfigurationDigest:  agent.ComputeDigest([]byte("child-batch-config")),
		})
		if err != nil {
			t.Fatal(err)
		}
		definition, err = NewDefinition(DefinitionConfig{
			Name: "interaction.child_batch", Description: "Exercise the child call protocol.",
			MaxModelCalls: 2, Tools: tools, ToolBudget: agent.Budget{Steps: 10, Effects: 10, Signals: 10},
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
			DirectResultEligible: kind == childCallsTool, ChildBatch: batch},
	}
	captured, err := encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := definition.Restore(captured)
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
	Key    agent.ChildKey      `json:"key"`
	Result childResultTestWire `json:"result"`
}

type childResultTestWire struct {
	ProcessID   agent.ProcessID `json:"process_id"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  time.Time       `json:"finished_at"`
	Output      agent.Output    `json:"output"`
	Termination json.RawMessage `json:"termination"`
}

func childBatchTestSignal(t testing.TB, waitID agent.WaitID, payload any) agent.Signal {
	t.Helper()
	encoded, err := json.Marshal(struct {
		ID      string       `json:"id"`
		WaitID  agent.WaitID `json:"wait_id"`
		Payload any          `json:"payload"`
	}{ID: "signal:child-batch", WaitID: waitID, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var signal agent.Signal
	if err := json.Unmarshal(encoded, &signal); err != nil {
		t.Fatal(err)
	}
	return signal
}
