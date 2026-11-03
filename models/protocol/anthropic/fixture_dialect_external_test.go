package anthropic_test

import wire "github.com/Tangerg/scope/models/protocol/anthropic"

func testMessagesDialect() wire.Dialect {
	return wire.Dialect{Provider: "anthropic", Revision: wire.MessagesRevision20261021, APIKeyScheme: wire.APIKeySchemeBearer, NativeJSONSchema: true}
}
