package revai

import "time"

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "revai"
)

const (
	RequestExtensionKey  = Provider + "/request"
	ResponseExtensionKey = Provider + "/response"
	DefaultBaseURL       = "https://api.rev.ai/speechtotext/v1"
	DefaultPollInterval  = 3 * time.Second
	DefaultPollTimeout   = 30 * time.Minute
)

const (
	ModelMachine = "machine"
	ModelHuman   = "human"
)
