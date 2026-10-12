package prodia

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "Prodia"
)

const (
	ImageRequestExtensionKey = "prodia/image_request"

	// DefaultBaseURL is Prodia's v2 inference endpoint.
	DefaultBaseURL = "https://inference.prodia.com/v2"
)

// Current official text-to-image job type identifiers.
const (
	JobFlux2DevTextToImage        = "inference.flux-2.dev.txt2img.v1"
	JobFlux2FlexTextToImage       = "inference.flux-2.flex.txt2img.v1"
	JobFlux2Klein4BTextToImage    = "inference.flux-2.klein.4b.txt2img.v1"
	JobFlux2Klein9BTextToImage    = "inference.flux-2.klein.9b.txt2img.v1"
	JobFlux2MaxTextToImage        = "inference.flux-2.max.txt2img.v1"
	JobFlux2ProTextToImage        = "inference.flux-2.pro.txt2img.v1"
	JobFluxFastSchnellTextToImage = "inference.flux-fast.schnell.txt2img.v2"
	JobRecraftV4TextToImage       = "inference.recraft.v4.txt2img.v1"
)
