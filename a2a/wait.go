package a2a

// WaitIDMetadataKey and AnswerSchemaMetadataKey bind a follow-up message to a
// concrete Scope wait. The schema is carried as exact JSON text, avoiding SDK
// conversions of numbers inside generic metadata.
const WaitIDMetadataKey = "scope.wait_id"
const AnswerSchemaMetadataKey = "scope.answer_schema"

// ExternalWaitExtension identifies the optional schema-aware, explicitly addressed
// follow-up supported by managed Scope tasks.
const ExternalWaitExtension = "urn:scope:a2a:external-wait:v1"
