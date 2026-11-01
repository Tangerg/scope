package anthropic

func testMessagesDialect() Dialect {
	return Dialect{Provider: "anthropic", MaxTemperature: 1, RejectTopK: true, RejectTopP: true, NativeJSONSchema: true}
}
