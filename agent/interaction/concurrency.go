package interaction

import (
	"context"
	"fmt"
	"sync"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type preparedToolCall struct {
	call       chat.ToolCall
	binding    *boundTool
	invocation tool.Invocation
	rejection  *chat.ToolResult
}

type toolConcurrencyPlan struct {
	concurrent bool
	key        string
}

type toolCallOutcome struct {
	result              chat.ToolResult
	advertisedToolNames []string
	required            *ToolInputRequest
	err                 error
}

func (d *Dispatcher) planToolConcurrency(calls []preparedToolCall) ([]toolConcurrencyPlan, error) {
	plans := make([]toolConcurrencyPlan, len(calls))
	for index := range calls {
		call := calls[index]
		if call.binding == nil || call.rejection != nil || call.binding.concurrent == nil {
			continue
		}
		key, concurrent, err := concurrencyDeclaration(call.binding.concurrent, call.invocation)
		if err != nil {
			return nil, fmt.Errorf("interaction: tool call %q concurrency: %w", call.call.ID, err)
		}
		plans[index] = toolConcurrencyPlan{concurrent: concurrent, key: key}
	}
	return plans, nil
}

func concurrencyDeclaration(
	capability ConcurrentTool,
	invocation tool.Invocation,
) (key string, concurrent bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			key = ""
			concurrent = false
			err = fmt.Errorf("capability panicked: %v", recovered)
		}
	}()
	key, concurrent = capability.ConcurrencyKey(invocation)
	return key, concurrent, nil
}

// concurrentBatchEnd returns the longest consecutive range that may overlap.
// One exclusive call forms its own batch; duplicate non-empty keys establish a
// boundary so no same-resource calls are ever active together.
func concurrentBatchEnd(plans []toolConcurrencyPlan, start int) int {
	if start < 0 || start >= len(plans) || !plans[start].concurrent {
		return start + 1
	}
	claimed := make(map[string]struct{})
	if plans[start].key != "" {
		claimed[plans[start].key] = struct{}{}
	}
	end := start + 1
	for end < len(plans) && plans[end].concurrent {
		key := plans[end].key
		if key != "" {
			if _, exists := claimed[key]; exists {
				break
			}
			claimed[key] = struct{}{}
		}
		end++
	}
	return end
}

func (d *Dispatcher) callToolBatch(
	ctx context.Context,
	request agent.EffectRequest,
	modelCallSequence uint32,
	firstToolCallIndex uint32,
	calls []preparedToolCall,
) []toolCallOutcome {
	outcomes := make([]toolCallOutcome, len(calls))
	if len(calls) == 1 {
		outcomes[0].result, outcomes[0].advertisedToolNames,
			outcomes[0].required, outcomes[0].err = d.callTool(
			ctx, request, modelCallSequence, firstToolCallIndex, calls[0],
		)
		return outcomes
	}

	limit := min(d.maxConcurrentToolCalls, len(calls))
	jobs := make(chan int, len(calls))
	var group sync.WaitGroup
	for range limit {
		group.Go(func() {
			for index := range jobs {
				outcomes[index].result, outcomes[index].advertisedToolNames,
					outcomes[index].required, outcomes[index].err = d.callTool(
					ctx,
					request,
					modelCallSequence,
					firstToolCallIndex+uint32(index),
					calls[index],
				)
			}
		})
	}
	for index := range calls {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	return outcomes
}
