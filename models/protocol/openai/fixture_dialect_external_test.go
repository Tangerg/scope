package openai_test

import wire "github.com/Tangerg/scope/models/protocol/openai"

func testChatDialect() wire.Dialect {
	return wire.Dialect{Provider: "openai", TokenLimitField: wire.TokenLimitMaxCompletionTokens, PrepareRequest: wire.RequestStreamUsage}
}
func testResponsesDialect() wire.ResponsesDialect { return wire.ResponsesDialect{Provider: "openai"} }
