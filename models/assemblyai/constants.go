package assemblyai

import (
	"time"
)

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "assemblyai"
)

const (
	RequestExtensionKey  = Provider + "/request"
	ResponseExtensionKey = Provider + "/response"

	DefaultBaseURL      = "https://api.assemblyai.com/v2"
	DefaultPollInterval = 2 * time.Second
	DefaultPollTimeout  = 30 * time.Minute
)

const (
	ModelUniversal3Point5Pro = "universal-3-5-pro"
	ModelUniversal2          = "universal-2"
)
