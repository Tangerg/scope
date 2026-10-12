package replicate

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "Replicate"
)

const (
	ImageRequestExtensionKey   = "replicate/image_request"
	SpeechRequestExtensionKey  = "replicate/speech_request"
	ImageResponseExtensionKey  = "replicate/image_response"
	SpeechResponseExtensionKey = "replicate/speech_response"

	// DefaultBaseURL is Replicate's production API root.
	DefaultBaseURL = "https://api.replicate.com/v1"

	// DefaultPollIntervalSeconds is how long Call waits between
	// status polls. Image jobs on Replicate take anywhere from 1s
	// (flux-schnell) to 30s (flux-1.1-pro-ultra, SDXL with high
	// step count); 1s is short enough to hide tail latency without
	// hammering the API.
	DefaultPollIntervalSeconds = 1

	// DefaultPollTimeoutSeconds is the wall-clock cap on a single
	// image generation. Bumps above 120s should target slow models
	// like SDXL with high inference steps or large-batch num_outputs.
	DefaultPollTimeoutSeconds = 180

	// DefaultTTSPollTimeoutSeconds is the wall-clock cap on a TTS
	// job. Bark and especially Tortoise-TTS can spend a long time
	// on cold starts plus generation, so the cap is set higher than image.
	DefaultTTSPollTimeoutSeconds = 300
)

// Official image model ids on Replicate. The unversioned owner/name form is
// valid only for official models and routes through the official-model
// prediction endpoint. Bind each id to its current OpenAPI schema before using
// it with ImageModel.
const (
	// ModelFluxSchnell is the cheapest / fastest FLUX (4-step
	// distilled, ~1s).
	ModelFluxSchnell = "black-forest-labs/flux-schnell"

	// ModelFluxDev is the open-weights FLUX dev model.
	ModelFluxDev = "black-forest-labs/flux-dev"

	// ModelFluxPro is the original commercial FLUX (50 steps).
	ModelFluxPro = "black-forest-labs/flux-pro"

	// ModelFlux11Pro (flux-1.1-pro) is the newer pro variant with
	// improved prompt adherence.
	ModelFlux11Pro = "black-forest-labs/flux-1.1-pro"

	// ModelFlux11ProUltra (flux-1.1-pro-ultra) targets up-to-4MP
	// renders.
	ModelFlux11ProUltra = "black-forest-labs/flux-1.1-pro-ultra"

	// ModelFluxKontextPro / Max are FLUX's instruction-driven editing
	// models — pass an input image via Extra "input_image" + a prompt
	// describing the edit.
	ModelFluxKontextPro = "black-forest-labs/flux-kontext-pro"
	ModelFluxKontextMax = "black-forest-labs/flux-kontext-max"

	// ModelIdeogramV2 is Ideogram V2 — strong on typography and
	// poster compositions.
	ModelIdeogramV2 = "ideogram-ai/ideogram-v2"

	// ModelIdeogramV2Turbo is the fast / cheap Ideogram variant.
	ModelIdeogramV2Turbo = "ideogram-ai/ideogram-v2-turbo"
)

// ModelXTTSV2 is a version-pinned community model id. Community-model
// predictions require an immutable version; using owner/name without a version
// would incorrectly target Replicate's official-model endpoint.
const (
	ModelXTTSV2 = "lucataco/xtts-v2:684bc3855b37866c0c65add2ff39c78f3dea3f4ff103a436465326e0f438d55e"
)
