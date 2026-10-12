package vertexai

const (
	Provider = "VertexAI"

	RequestExtensionKey               = "vertexai/request"
	ResponseExtensionKey              = "vertexai/response"
	SpeechRequestExtensionKey         = "vertexai/speech_request"
	SpeechResponseExtensionKey        = "vertexai/speech_response"
	TranscriptionRequestExtensionKey  = "vertexai/transcription_request"
	TranscriptionResponseExtensionKey = "vertexai/transcription_response"
	EmbeddingRequestExtensionKey      = "vertexai/embedding_request"
	EmbeddingResponseExtensionKey     = "vertexai/embedding_response"
	ImageRequestExtensionKey          = "vertexai/image_request"
	ImageResponseExtensionKey         = "vertexai/image_response"

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
