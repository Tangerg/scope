package anthropic

func testMessagesDialect() Dialect {
	return Dialect{Provider: "anthropic", Revision: MessagesRevision20261021, APIKeyScheme: APIKeySchemeBearer, NativeJSONSchema: true}
}
