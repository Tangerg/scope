package moonshot

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "Moonshot"
)

// OpenAI-compatible endpoints.
const (
	// BaseURL is the domestic OpenAI-compat endpoint.
	BaseURL = "https://api.moonshot.cn/v1"

	// BaseURLIntl is the international OpenAI-compat endpoint.
	BaseURLIntl = "https://api.moonshot.ai/v1"
)

// Anthropic-compatible endpoints. anthropic-sdk-go appends "v1/messages"
// to the configured base.
const (
	// BaseURLAnthropic is the domestic Anthropic-compat endpoint.
	BaseURLAnthropic = "https://api.moonshot.cn/anthropic"

	// BaseURLIntlAnthropic is the international Anthropic-compat endpoint.
	BaseURLIntlAnthropic = "https://api.moonshot.ai/anthropic"
)

// Current Chat model ids. See https://platform.kimi.com/docs/models.
const (
	ModelK3               = "kimi-k3"
	ModelK27Code          = "kimi-k2.7-code"
	ModelK27CodeHighSpeed = "kimi-k2.7-code-highspeed"
	ModelK26              = "kimi-k2.6"
)
