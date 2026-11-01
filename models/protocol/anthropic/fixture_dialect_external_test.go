package anthropic_test

import wire "github.com/Tangerg/scope/models/protocol/anthropic"

func testMessagesDialect() wire.Dialect {
	return wire.Dialect{Provider: "anthropic", MaxTemperature: 1, RejectTopK: true, RejectTopP: true, NativeJSONSchema: true}
}
