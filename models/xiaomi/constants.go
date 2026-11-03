package xiaomi

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "xiaomi"
)

// OpenAI-compatible endpoint.
const (
	// BaseURL is the MiMo OpenAI-compatible endpoint.
	BaseURL = "https://api.xiaomimimo.com/v1"
)

// Anthropic-compatible endpoint. anthropic-sdk-go appends
// "v1/messages" to the supplied BaseURL so the full URL ends at
// https://api.xiaomimimo.com/anthropic/v1/messages.
const (
	// BaseURLAnthropic is the MiMo Anthropic-compatible endpoint.
	BaseURLAnthropic = "https://api.xiaomimimo.com/anthropic"
)

// Current Chat model ids. See
// https://mimo.mi.com/static/docs/quick-start/summary/model.md.
const (
	ModelV25Pro = "mimo-v2.5-pro"
	ModelV25    = "mimo-v2.5"
)
