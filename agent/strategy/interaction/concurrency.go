package interaction

import (
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

func (t toolManifest) planCalls(calls []chat.ToolCall) ([]toolConcurrencyPlan, error) {
	plans := make([]toolConcurrencyPlan, len(calls))
	for index, call := range calls {
		entry, found := t.entries[call.Name]
		if !found {
			continue
		}
		plan, err := entry.plan(call)
		if err != nil {
			return nil, fmt.Errorf("interaction: tool call %q concurrency: %w", call.ID, err)
		}
		plans[index] = plan
	}
	return plans, nil
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
