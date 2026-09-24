package deepseek

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "DeepSeek"
)

const (
	// BaseURL is DeepSeek's production endpoint. Equivalent to
	// "https://api.deepseek.com/v1" — DeepSeek accepts both.
	BaseURL = "https://api.deepseek.com"
)

// Production model IDs. See
// https://api-docs.deepseek.com/quick_start/pricing for the current
// pricing and context-window limits.
const (
	ModelFlash = "deepseek-flash"
	ModelV4Pro = "deepseek-v4-pro"
)
