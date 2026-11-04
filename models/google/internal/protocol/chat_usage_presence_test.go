package protocol_test

import (
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestChatUsagePreservesBreakdownPresence(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, test := range []struct {
			name      string
			fields    string
			reasoning *int64
			cached    *int64
		}{
			{name: "absent"},
			{name: "null", fields: `,"thoughtsTokenCount":null,"cachedContentTokenCount":null`},
			{name: "explicit zero", fields: `,"thoughtsTokenCount":0,"cachedContentTokenCount":0`, reasoning: new(int64(0)), cached: new(int64(0))},
			{name: "reasoning only", fields: `,"thoughtsTokenCount":2`, reasoning: new(int64(2))},
			{name: "cache only", fields: `,"cachedContentTokenCount":1`, cached: new(int64(1))},
			{name: "positive", fields: `,"thoughtsTokenCount":2,"cachedContentTokenCount":1`, reasoning: new(int64(2)), cached: new(int64(1))},
		} {
			t.Run(provider+"/"+test.name, func(t *testing.T) {
				model := newNativeContractChat(t, provider, nativeChatChunks(
					`{"candidates":[{"index":0,"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}`,
					`{"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"toolUsePromptTokenCount":1`+test.fields+`}}`,
				))
				request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
				response, err := model.Call(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				streamed, err := corechat.Collect(model.Stream(t.Context(), request))
				if err != nil {
					t.Fatal(err)
				}
				for _, result := range []*corechat.Response{response, streamed} {
					usage := result.Metadata.Usage
					wantOutput := int64(2)
					if test.reasoning != nil {
						wantOutput += *test.reasoning
					}
					if usage == nil || usage.InputTokens != 4 || usage.OutputTokens != wantOutput {
						t.Fatalf("inclusive usage = %+v", usage)
					}
					if (usage.ReasoningTokens == nil) != (test.reasoning == nil) || usage.ReasoningTokens != nil && *usage.ReasoningTokens != *test.reasoning {
						t.Fatalf("reasoning presence/value = %v; want %v", usage.ReasoningTokens, test.reasoning)
					}
					if (usage.CacheReadInputTokens == nil) != (test.cached == nil) || usage.CacheReadInputTokens != nil && *usage.CacheReadInputTokens != *test.cached {
						t.Fatalf("cache presence/value = %v; want %v", usage.CacheReadInputTokens, test.cached)
					}
				}
			})
		}
	}
}
