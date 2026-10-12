package elevenlabs

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "ElevenLabs"
)

const (
	SpeechRequestExtensionKey         = "elevenlabs/speech_request"
	SpeechResponseExtensionKey        = "elevenlabs/speech_response"
	TranscriptionRequestExtensionKey  = "elevenlabs/transcription_request"
	TranscriptionResponseExtensionKey = "elevenlabs/transcription_response"

	// DefaultBaseURL is ElevenLabs' production REST endpoint.
	DefaultBaseURL = "https://api.elevenlabs.io/v1"
)

// These are the provider values this adapter recognizes.
const (
	ModelElevenV3       = "eleven_v3"
	ModelMultilingualV2 = "eleven_multilingual_v2"
	ModelFlashV2        = "eleven_flash_v2"
	ModelFlashV2Point5  = "eleven_flash_v2_5"
	ModelScribeV2       = "scribe_v2"
	ModelScribeV1       = "scribe_v1"
)
