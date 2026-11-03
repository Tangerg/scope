package elevenlabs

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "elevenlabs"
)

const (
	SpeechRequestExtensionKey         = Provider + "/speech_request"
	SpeechResponseExtensionKey        = Provider + "/speech_response"
	TranscriptionRequestExtensionKey  = Provider + "/transcription_request"
	TranscriptionResponseExtensionKey = Provider + "/transcription_response"

	// DefaultBaseURL is ElevenLabs' production REST endpoint.
	DefaultBaseURL = "https://api.elevenlabs.io/v1"
)

// Model IDs are conveniences, not an admission allowlist.
const (
	ModelElevenV3       = "eleven_v3"
	ModelMultilingualV2 = "eleven_multilingual_v2"
	ModelFlashV2        = "eleven_flash_v2"
	ModelFlashV2Point5  = "eleven_flash_v2_5"
	ModelScribeV2       = "scribe_v2"
	ModelScribeV1       = "scribe_v1"
)
