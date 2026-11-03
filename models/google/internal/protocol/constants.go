package protocol

const GoogleProviderID = "google"

const DefaultBaseURL = "https://generativelanguage.googleapis.com"

const (
	SpeechRequestExtensionKey         = GoogleProviderID + "/speech_request"
	SpeechResponseExtensionKey        = GoogleProviderID + "/speech_response"
	TranscriptionRequestExtensionKey  = GoogleProviderID + "/transcription_request"
	TranscriptionResponseExtensionKey = GoogleProviderID + "/transcription_response"
	EmbeddingRequestExtensionKey      = GoogleProviderID + "/embedding_request"
	EmbeddingResponseExtensionKey     = GoogleProviderID + "/embedding_response"
	ImageRequestExtensionKey          = GoogleProviderID + "/image_request"
	ImageResponseExtensionKey         = GoogleProviderID + "/image_response"
)

const (
	ModelGemini36Flash      = "gemini-3.6-flash"
	ModelGemini35Flash      = "gemini-3.5-flash"
	ModelGemini35FlashLite  = "gemini-3.5-flash-lite"
	ModelGemini31ProPreview = "gemini-3.1-pro-preview"

	ModelGemini25FlashPreviewTTS = "gemini-2.5-flash-preview-tts"
	ModelGemini25ProPreviewTTS   = "gemini-2.5-pro-preview-tts"
	ModelGemini31FlashTTSPreview = "gemini-3.1-flash-tts-preview"

	ModelGemini25FlashImage     = "gemini-2.5-flash-image"
	ModelGemini3ProImage        = "gemini-3-pro-image"
	ModelGemini31FlashImage     = "gemini-3.1-flash-image"
	ModelGemini31FlashLiteImage = "gemini-3.1-flash-lite-image"

	ModelGeminiEmbedding2 = "gemini-embedding-2"
)
