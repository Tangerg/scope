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
