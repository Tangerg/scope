package fireworks

// Exported identifiers keep provider-owned names and defaults out of caller literals.
const (
	Provider = "Fireworks"

	BaseURL = "https://api.fireworks.ai/inference/v1"
)

// Current serverless model ids. See https://fireworks.ai/models for the live
// catalog — Fireworks prefixes model ids with "accounts/fireworks/models/".
const (
	ModelGPTOSS120B = "accounts/fireworks/models/gpt-oss-120b"
	ModelKimiK3     = "accounts/fireworks/models/kimi-k3"
	ModelGLM53      = "accounts/fireworks/models/glm-5p3"
)
