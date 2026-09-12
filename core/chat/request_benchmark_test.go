package chat_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func BenchmarkRequestJSONHistory(b *testing.B) {
	for _, count := range []int{16, 64, 256} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			messages := make([]chat.Message, 0, count*3)
			for i := range count {
				call := chat.ToolCall{ID: fmt.Sprint(i), Name: "lookup", Arguments: `{"key":"value"}`}
				messages = append(messages, chat.NewUserMessage(chat.NewTextPart(strings.Repeat("question ", 128))), chat.NewAssistantMessage(chat.NewToolCallPart(call)), chat.NewToolMessage(chat.ToolResult{ID: call.ID, Name: call.Name, Output: chat.NewTextToolOutput(strings.Repeat("evidence ", 128))}))
			}
			request, err := chat.NewRequest(messages...)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := json.Marshal(request); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
