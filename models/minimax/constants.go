package minimax

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "MiniMax"
)

// OpenAI-compatible endpoints.
const (
	// BaseURLIntl is the international OpenAI-compat endpoint.
	BaseURLIntl = "https://api.minimax.io/v1"

	// BaseURLChina is the China OpenAI-compat endpoint.
	BaseURLChina = "https://api.minimaxi.com/v1"
)

// Anthropic-compatible endpoints. The anthropic-sdk-go client appends
// "v1/messages" to the supplied BaseURL so the full request URL ends
// at, e.g., https://api.minimax.io/anthropic/v1/messages.
const (
	// BaseURLIntlAnthropic is the international Anthropic-compat endpoint.
	BaseURLIntlAnthropic = "https://api.minimax.io/anthropic"

	// BaseURLChinaAnthropic is the China Anthropic-compat endpoint.
	BaseURLChinaAnthropic = "https://api.minimaxi.com/anthropic"
)

// Current Chat model ids. See
// https://platform.minimaxi.com/docs/guides/models-intro.
const (
	ModelM3           = "MiniMax-M3"
	ModelM27          = "MiniMax-M2.7"
	ModelM27HighSpeed = "MiniMax-M2.7-highspeed"
	ModelM25          = "MiniMax-M2.5"
	ModelM25HighSpeed = "MiniMax-M2.5-highspeed"
	ModelM21          = "MiniMax-M2.1"
	ModelM2           = "MiniMax-M2"
)
