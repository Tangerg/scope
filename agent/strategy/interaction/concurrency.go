package interaction

import (
	"context"
	"fmt"

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

func (t toolManifestEntry) plan(call chat.ToolCall) (toolConcurrencyPlan, error) {
	if t.concurrent == nil {
		return toolConcurrencyPlan{}, nil
	}
	invocation, err := t.contract.Prepare(call)
	if err != nil {
		// Invalid arguments receive an ordinary rejection from the child; they
		// confer no authority to overlap other calls.
		return toolConcurrencyPlan{}, nil
	}
	key, concurrent, err := concurrencyDeclaration(t.concurrent, invocation)
	return toolConcurrencyPlan{concurrent: concurrent, key: key}, err
}

func concurrencyDeclaration(
	policy func(tool.Invocation) (string, bool),
	invocation tool.Invocation,
) (key string, concurrent bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			key = ""
			concurrent = false
			err = fmt.Errorf("capability panicked: %v", recovered)
		}
	}()
	key, concurrent = policy(invocation)
	return key, concurrent, nil
}

// concurrentBatchEnd inspects only the next group and its first boundary.
// Classifying later calls again at every boundary makes exclusive runs quadratic.
func (t toolManifest) concurrentBatchEnd(ctx context.Context, calls []chat.ToolCall) (int, error) {
	claimed := make(map[string]struct{})
	for index, call := range calls {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		entry, found := t.entries[call.Name]
		if !found {
			return index, nil
		}
		plan, err := entry.plan(call)
		if err != nil {
			return 0, fmt.Errorf("interaction: tool call %q concurrency: %w", call.ID, err)
		}
		if !plan.concurrent {
			if index == 0 {
				return 1, nil
			}
			return index, nil
		}
		if plan.key != "" {
			if _, duplicate := claimed[plan.key]; duplicate {
				return index, nil
			}
			claimed[plan.key] = struct{}{}
		}
	}
	return len(calls), nil
}
