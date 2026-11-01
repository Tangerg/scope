package anthropic

func testMessagesDialect() Dialect {
	return Dialect{Provider: "anthropic", NativeJSONSchema: true}
}
