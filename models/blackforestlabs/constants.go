package blackforestlabs

import "time"

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "blackforestlabs"
)

const (
	ImageRequestExtensionKey = Provider + "/image_request"

	DefaultBaseURL      = "https://api.bfl.ai/v1"
	DefaultPollInterval = time.Second
	DefaultPollTimeout  = 2 * time.Minute
)

const (
	ModelFlux2Max            = "flux-2-max"
	ModelFlux2ProPreview     = "flux-2-pro-preview"
	ModelFlux2Pro            = "flux-2-pro"
	ModelFlux2Flex           = "flux-2-flex"
	ModelFlux2Klein4B        = "flux-2-klein-4b"
	ModelFlux2Klein9BPreview = "flux-2-klein-9b-preview"
	ModelFlux2Klein9B        = "flux-2-klein-9b"
	ModelFluxKontextMax      = "flux-kontext-max"
	ModelFluxKontextPro      = "flux-kontext-pro"
	ModelFlux11ProUltra      = "flux-pro-1.1-ultra"
	ModelFlux11Pro           = "flux-pro-1.1"
	ModelFluxPro             = "flux-pro"
	ModelFluxDev             = "flux-dev"
)
