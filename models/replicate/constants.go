package replicate

import "time"

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "replicate"
)

const (
	ImageRequestExtensionKey   = Provider + "/image_request"
	SpeechRequestExtensionKey  = Provider + "/speech_request"
	ImageResponseExtensionKey  = Provider + "/image_response"
	SpeechResponseExtensionKey = Provider + "/speech_response"

	DefaultBaseURL           = "https://api.replicate.com/v1"
	DefaultPollInterval      = time.Second
	DefaultImagePollTimeout  = 3 * time.Minute
	DefaultSpeechPollTimeout = 5 * time.Minute
)

const (
	ModelFluxSchnell     = "black-forest-labs/flux-schnell"
	ModelFluxDev         = "black-forest-labs/flux-dev"
	ModelFluxPro         = "black-forest-labs/flux-pro"
	ModelFlux11Pro       = "black-forest-labs/flux-1.1-pro"
	ModelFlux11ProUltra  = "black-forest-labs/flux-1.1-pro-ultra"
	ModelFluxKontextPro  = "black-forest-labs/flux-kontext-pro"
	ModelFluxKontextMax  = "black-forest-labs/flux-kontext-max"
	ModelIdeogramV2      = "ideogram-ai/ideogram-v2"
	ModelIdeogramV2Turbo = "ideogram-ai/ideogram-v2-turbo"
)

// ModelXTTSV2 is a version-pinned community model id. Community-model
// predictions require an immutable version; using owner/name without a version
// would incorrectly target Replicate's official-model endpoint.
const (
	ModelXTTSV2 = "lucataco/xtts-v2:684bc3855b37866c0c65add2ff39c78f3dea3f4ff103a436465326e0f438d55e"
)
