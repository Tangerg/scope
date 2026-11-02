package interaction

import (
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func cloneToolDefinitions(definitions []tool.Definition) []tool.Definition {
	if definitions == nil {
		return nil
	}
	cloned := make([]tool.Definition, len(definitions))
	for index, definition := range definitions {
		cloned[index] = definition.Clone()
	}
	return cloned
}

func cloneToolResults(results []chat.ToolResult) []chat.ToolResult {
	if results == nil {
		return nil
	}
	cloned := make([]chat.ToolResult, len(results))
	for index := range results {
		cloned[index] = results[index].Clone()
	}
	return cloned
}
