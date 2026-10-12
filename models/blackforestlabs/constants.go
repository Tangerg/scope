package blackforestlabs

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "BlackForestLabs"
)

const (
	ImageRequestExtensionKey = "blackforestlabs/image_request"

	// DefaultBaseURL is BFL's production endpoint.
	DefaultBaseURL = "https://api.bfl.ai/v1"

	// DefaultPollInterval / DefaultPollTimeout configure how Call waits
	// for an async generation to complete. BFL's typical render time
	// is 3–15s for flux-dev / flux-pro / flux-schnell.
	DefaultPollIntervalSeconds = 1
	DefaultPollTimeoutSeconds  = 120
)

// Current official image-generation endpoint identifiers.
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
