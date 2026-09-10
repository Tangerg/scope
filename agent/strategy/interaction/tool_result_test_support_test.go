package interaction_test

import "github.com/Tangerg/scope/core/chat"

func toolResultText(result *chat.ToolResult) (string, bool) {
	if result == nil {
		return "", false
	}
	return result.Output.Text()
}
