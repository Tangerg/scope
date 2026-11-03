package gladia

import "time"

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "gladia"
)

const (
	RequestExtensionKey  = Provider + "/request"
	ResponseExtensionKey = Provider + "/response"
	DefaultBaseURL       = "https://api.gladia.io/v2"
	DefaultPollInterval  = 2 * time.Second
	DefaultPollTimeout   = 30 * time.Minute
)

// These are the provider values this adapter recognizes.
const (
	ModelSolaria3 = "solaria-3"
	ModelSolaria1 = "solaria-1"
)
