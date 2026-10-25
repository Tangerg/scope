package openrouter

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "OpenRouter"
)

const (
	BaseURL          = "https://openrouter.ai/api/v1"
	BaseURLAnthropic = "https://openrouter.ai/api"

	// ModelAuto delegates per-request model selection to OpenRouter's
	// task-aware Auto Router.
	ModelAuto = "openrouter/auto"
)

const (
	// HeaderReferer is the standard HTTP Referer header used by
	// OpenRouter for app attribution / analytics. Pass your app's
	// homepage URL.
	HeaderReferer = "HTTP-Referer"

	// HeaderAppTitle is the X-Title header OpenRouter shows on
	// leaderboards / rankings. Pass your app's display name.
	HeaderAppTitle = "X-Title"
)
