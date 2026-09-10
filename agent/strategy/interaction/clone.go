package interaction

import "github.com/Tangerg/scope/core/chat"

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
