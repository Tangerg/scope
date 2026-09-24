package xai

// Exported identifiers keep provider-owned names and defaults out of caller literals.
const (
	Provider = "xAI"

	BaseURL = "https://api.x.ai/v1"
)

// See https://docs.x.ai/developers/models for the current catalog.
const (
	ModelGrok45      = "grok-4.5"
	ModelGrok43      = "grok-4.3"
	ModelGrokBuild01 = "grok-build-0.1"
)
