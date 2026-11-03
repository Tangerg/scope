package vertexai

const (
	Provider = "vertexai"

	RequestExtensionKey               = Provider + "/request"
	ResponseExtensionKey              = Provider + "/response"
	SpeechRequestExtensionKey         = Provider + "/speech_request"
	SpeechResponseExtensionKey        = Provider + "/speech_response"
	TranscriptionRequestExtensionKey  = Provider + "/transcription_request"
	TranscriptionResponseExtensionKey = Provider + "/transcription_response"
	EmbeddingRequestExtensionKey      = Provider + "/embedding_request"
	EmbeddingResponseExtensionKey     = Provider + "/embedding_response"
	ImageRequestExtensionKey          = Provider + "/image_request"
	ImageResponseExtensionKey         = Provider + "/image_response"

	ModelGemini25FlashImage = "gemini-2.5-flash-image"
	ModelGemini3ProImage    = "gemini-3-pro-image"
)

// Common Vertex AI regions. See
// https://cloud.google.com/vertex-ai/generative-ai/docs/learn/locations
// for the full list — coverage varies by model.
const (
	// LocationUSCentral1 is the global default for most generative
	// models (Gemini, Imagen, embedding).
	LocationUSCentral1 = "us-central1"

	// LocationGlobal is the multi-region endpoint — useful for
	// latency-sensitive workloads that can tolerate Google's
	// internal routing.
	LocationGlobal = "global"

	// LocationEuropeWest4 hosts Gemini and Imagen in the EU.
	LocationEuropeWest4 = "europe-west4"

	// LocationAsiaSoutheast1 (Singapore) hosts Gemini in APAC.
	LocationAsiaSoutheast1 = "asia-southeast1"

	// LocationAsiaNortheast1 (Tokyo) hosts Gemini in Japan.
	LocationAsiaNortheast1 = "asia-northeast1"
)
