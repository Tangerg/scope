package deepgram

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "deepgram"
)

const (
	SpeechRequestExtensionKey         = Provider + "/speech_request"
	SpeechResponseExtensionKey        = Provider + "/speech_response"
	TranscriptionRequestExtensionKey  = Provider + "/transcription_request"
	TranscriptionResponseExtensionKey = Provider + "/transcription_response"

	// DefaultBaseURL is Deepgram's production REST endpoint.
	DefaultBaseURL = "https://api.deepgram.com/v1"
)
