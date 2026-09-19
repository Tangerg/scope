package interaction

import (
	"github.com/Tangerg/scope/agent"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type preparedToolCall struct {
	call       chat.ToolCall
	binding    *boundTool
	invocation tool.Invocation
	rejection  *chat.ToolResult
}

func (p preparedToolCall) completion(result chat.ToolResult, rejected bool, advertised []string) *toolCallResult {
	return &toolCallResult{Result: result.Clone(), Rejected: rejected,
		Direct: p.binding != nil && p.binding.direct && !result.IsError, AdvertisedToolNames: advertised}
}

type toolConcurrencyPlan struct {
	concurrent bool
	key        string
}

func concurrencyDeclaration(
	policy func(tool.Invocation) (string, bool),
	invocation tool.Invocation,
) (key string, concurrent bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			key = ""
			concurrent = false
			err = &agent.CallbackPanicError{Operation: "ConcurrentTool policy", Value: recovered}
		}
	}()
	key, concurrent = policy(invocation)
	return key, concurrent, nil
}
