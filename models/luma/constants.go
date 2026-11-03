package luma

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "luma"
)

const (
	ImageRequestExtensionKey   = Provider + "/image_request"
	ResponseExtensionKey       = Provider + "/response"
	DefaultBaseURL             = "https://agents.lumalabs.ai/v1"
	DefaultPollIntervalSeconds = 2
	DefaultPollTimeoutSeconds  = 300
	ModelUni1                  = "uni-1"
	ModelUni1Max               = "uni-1-max"
)
