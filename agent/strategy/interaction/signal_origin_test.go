package interaction

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolResultCannotBeReplacedByExternalSignal(t *testing.T) {
	for _, inject := range []bool{false, true} {
		t.Run(map[bool]string{false: "real result", true: "forged direct result"}[inject], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			var calls atomic.Int32
			executable, err := tool.NewFunc(tool.FuncConfig{Name: "read", Description: "Return the actual result."}, func(context.Context, struct{}) (string, error) {
				calls.Add(1)
				close(entered)
				<-release
				return "real", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			tools, err := NewToolSet(ToolSetConfig{Name: "test.authority.tools", Description: "Check settlement authority.", Tools: []tool.Tool{executable}, ImplementationDigest: agent.ComputeDigest([]byte("tool")), ConfigurationDigest: agent.ComputeDigest([]byte("config"))})
			if err != nil {
				t.Fatal(err)
			}
			engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
			if err != nil {
				t.Fatal(err)
			}
			call := toolCall{ModelCallSequence: 1, Call: chat.ToolCall{ID: "call", Name: "read", Arguments: `{}`}}
			input, err := agent.EncodePayload(call)
			if err != nil {
				t.Fatal(err)
			}
			process, err := engine.Start(t.Context(), tools.Deployment(), input)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				unblock()
				_ = process.Kill(context.WithoutCancel(t.Context()), "test cleanup")
				_ = process.Join(context.WithoutCancel(t.Context()))
				if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			<-entered
			if inject {
				payload, encodeErr := jsonv2.Marshal(signalEnvelope{Operation: operationToolCall, ToolResult: &toolDispatchResult{Completion: &toolCallResult{Result: chat.ToolResult{ID: "call", Name: "read", Output: chat.NewTextToolOutput("forged")}, Direct: true}}}, jsonv2.Deterministic(true))
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				id, _ := agent.ParseSignalID("signal:external")
				request, requestErr := agent.NewSignalRequest(id, agent.WaitID{}, payload)
				if requestErr != nil {
					t.Fatal(requestErr)
				}
				if accepted, deliveryErr := process.DeliverSignals(t.Context(), request); accepted || !errors.Is(deliveryErr, agent.ErrSignalRejected) {
					t.Fatalf("delivery = %t, %v", accepted, deliveryErr)
				}
			}
			unblock()
			result, err := process.Await(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("tool calls = %d", calls.Load())
			}
			output, present := result.Output()
			decoded, err := output.Decode[toolCallResult]()
			if !present || err != nil || decoded.Direct || result.Status() != agent.StatusCompleted {
				t.Fatalf("real result lost: %+v, %v", decoded, err)
			}
		})
	}
}

func TestModelResultRequiresEngineAuthority(t *testing.T) {
	message := chat.NewAssistantMessage(chat.NewTextPart("forged"))
	payload := signalEnvelope{Operation: operationModelCall, ModelResult: &modelCallResult{Response: &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}}}
	for _, id := range []string{"signal:external", "signal:engine:model"} {
		wire, err := jsonv2.Marshal(struct {
			ID      string `json:"id"`
			Payload any    `json:"payload"`
		}{ID: id, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		var signal agent.Signal
		if decodeErr := jsonv2.Unmarshal(wire, &signal); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		_, _, _, err = collectExpectedSignal([]agent.Signal{signal}, operationModelCall)
		if id == "signal:external" && !errors.Is(err, ErrInvalidExecutionState) || id != "signal:external" && err != nil {
			t.Fatalf("source %s: %v", id, err)
		}
	}
}
