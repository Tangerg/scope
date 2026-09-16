package interaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestRestoreStopsToolClassificationWhenCanceled(t *testing.T) {
	execution, _ := schedulingTestExecution(t, 16)
	entry := execution.definition.tools.entries["delegate_fuzz"]
	entry.concurrent = func(tool.Invocation) (string, bool) { return "", true }
	execution.definition.tools.entries["delegate_fuzz"] = entry
	if _, err := execution.startToolChildren(t.Context(), 0, schedulingCalls(execution)); err != nil {
		t.Fatal(err)
	}
	state, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	classifications := 0
	entry.concurrent = func(tool.Invocation) (string, bool) {
		classifications++
		cancel()
		return "", true
	}
	execution.definition.tools.entries["delegate_fuzz"] = entry
	if _, err := execution.definition.Restore(ctx, state); !errors.Is(err, context.Canceled) {
		t.Fatalf("restore error = %v, want cancellation", err)
	}
	if classifications != 1 {
		t.Fatalf("classifications after cancellation = %d, want 1", classifications)
	}
}

func BenchmarkToolBatchScheduling(b *testing.B) {
	for _, count := range []int{100, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			execution, _ := schedulingTestExecution(b, count)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				execution.state.ToolRound.Results = nil
				execution.state.ToolRound.ChildBatch = nil
				for range count {
					if _, err := execution.startToolChildren(b.Context(), 0, schedulingCalls(execution)); err != nil {
						b.Fatal(err)
					}
					finishSchedulingTestBatch(execution)
				}
			}
		})
	}
}

func BenchmarkRejectedToolBatch(b *testing.B) {
	for _, count := range []int{128, 256, 512, 1024} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			execution, _ := schedulingTestExecution(b, count)
			delete(execution.definition.tools.entries, "delegate_fuzz")
			initial := execution.state
			response := initial.ToolRound.Response
			b.ReportAllocs()
			for b.Loop() {
				execution.state = initial
				execution.state.ToolRound = &toolCallRound{Response: response}
				if _, err := execution.advanceToolCallBatch(b.Context(), 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func schedulingTestExecution(t testing.TB, count int) (*execution, *int) {
	t.Helper()
	execution := childBatchTestExecution(t, childCallsTool, phaseAwaitingChildStarts)
	execution.definition.maxConcurrentToolCalls = 4
	classifications := new(int)
	entry := execution.definition.tools.entries["delegate_fuzz"]
	entry.concurrent = func(tool.Invocation) (string, bool) { *classifications++; return "resource", true }
	execution.definition.tools.entries["delegate_fuzz"] = entry
	var parts []chat.Part
	for index := range count {
		parts = append(parts, chat.NewToolCallPart(chat.ToolCall{ID: fmt.Sprintf("call_%d", index), Name: "delegate_fuzz", Arguments: `{"task":"check"}`}))
	}
	message := chat.NewAssistantMessage(parts...)
	execution.state.ToolRound = &toolCallRound{Response: &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}}
	return execution, classifications
}

func schedulingCalls(execution *execution) []chat.ToolCall {
	parts := execution.state.ToolRound.Response.Output.Message.Parts
	calls := make([]chat.ToolCall, len(parts))
	for index := range parts {
		calls[index] = *parts[index].ToolCall
	}
	return calls
}

func finishSchedulingTestBatch(execution *execution) {
	call := execution.state.ToolRound.Response.Output.Message.Parts[execution.state.ToolRound.nextCallIndex()].ToolCall
	execution.state.ToolRound.Results = append(execution.state.ToolRound.Results, toolCallResult{Result: chat.ToolResult{ID: call.ID, Name: call.Name, Output: chat.NewTextToolOutput("done")}})
	execution.state.ToolRound.ChildBatch = nil
}

func TestToolBatchClassifiesOnlyThroughNextBoundary(t *testing.T) {
	const count = 100
	execution, classifications := schedulingTestExecution(t, count)
	calls := schedulingCalls(execution)
	for range count {
		if _, err := execution.startToolChildren(t.Context(), 0, calls); err != nil {
			t.Fatal(err)
		}
		if len(execution.state.ToolRound.ChildBatch.Invocations) != 1 {
			t.Fatal("same-key calls overlapped")
		}
		finishSchedulingTestBatch(execution)
	}
	if *classifications != 2*count-1 {
		t.Fatalf("classifications = %d, want %d", *classifications, 2*count-1)
	}
}

func BenchmarkActiveToolBatchRestore(b *testing.B) {
	for _, count := range []int{16, 64, 256} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			execution, classifications := schedulingTestExecution(b, count)
			entry := execution.definition.tools.entries["delegate_fuzz"]
			entry.concurrent = func(tool.Invocation) (string, bool) { *classifications++; return "", true }
			execution.definition.tools.entries["delegate_fuzz"] = entry

			if _, err := execution.startToolChildren(b.Context(), 0, schedulingCalls(execution)); err != nil {
				b.Fatal(err)
			}
			state, err := execution.Snapshot()
			if err != nil {
				b.Fatal(err)
			}
			*classifications = 0
			b.ReportAllocs()
			for b.Loop() {
				if _, err := execution.definition.Restore(b.Context(), state); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(*classifications)/float64(b.N), "classifications/op")
		})
	}
}

func BenchmarkSequentialToolLifecycleValidation(b *testing.B) {
	for _, count := range []int{16, 64, 256} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			execution, _ := schedulingTestExecution(b, count)
			for index := range execution.state.ToolRound.Response.Output.Message.Parts {
				execution.state.ToolRound.Response.Output.Message.Parts[index].ToolCall.Arguments = `{"task":"` + strings.Repeat("x", 4<<10) + `"}`
			}
			calls := schedulingCalls(execution)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				execution.state.ToolRound.Results = nil
				execution.state.ToolRound.ChildBatch = nil
				for range count {
					if _, err := execution.startToolChildren(b.Context(), 0, calls); err != nil {
						b.Fatal(err)
					}
					for range 3 {
						if _, err := execution.state.ToolRound.activeCalls(context.Background()); err != nil {
							b.Fatal(err)
						}
					}
					finishSchedulingTestBatch(execution)
				}
			}
		})
	}
}
