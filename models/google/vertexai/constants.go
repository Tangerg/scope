package vertexai

const (
	Provider = "VertexAI"

	RequestExtensionKey               = protocolProvider + "/request"
	ResponseExtensionKey              = protocolProvider + "/response"
	SpeechRequestExtensionKey         = protocolProvider + "/speech_request"
	SpeechResponseExtensionKey        = protocolProvider + "/speech_response"
	TranscriptionRequestExtensionKey  = protocolProvider + "/transcription_request"
	TranscriptionResponseExtensionKey = protocolProvider + "/transcription_response"
	EmbeddingRequestExtensionKey      = protocolProvider + "/embedding_request"
	EmbeddingResponseExtensionKey     = protocolProvider + "/embedding_response"
	ImageRequestExtensionKey          = protocolProvider + "/image_request"
	ImageResponseExtensionKey         = protocolProvider + "/image_response"

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
