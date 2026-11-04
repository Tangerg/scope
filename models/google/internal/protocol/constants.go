package protocol

const GoogleProviderID = "google"

const DefaultBaseURL = "https://generativelanguage.googleapis.com"

const (
	RequestExtensionSuffix               = "/request"
	ResponseExtensionSuffix              = "/response"
	SpeechRequestExtensionSuffix         = "/speech_request"
	SpeechResponseExtensionSuffix        = "/speech_response"
	TranscriptionRequestExtensionSuffix  = "/transcription_request"
	TranscriptionResponseExtensionSuffix = "/transcription_response"
	EmbeddingRequestExtensionSuffix      = "/embedding_request"
	EmbeddingResponseExtensionSuffix     = "/embedding_response"
	ImageRequestExtensionSuffix          = "/image_request"
	ImageResponseExtensionSuffix         = "/image_response"
)

const (
	SpeechRequestExtensionKey         = GoogleProviderID + SpeechRequestExtensionSuffix
	SpeechResponseExtensionKey        = GoogleProviderID + SpeechResponseExtensionSuffix
	TranscriptionRequestExtensionKey  = GoogleProviderID + TranscriptionRequestExtensionSuffix
	TranscriptionResponseExtensionKey = GoogleProviderID + TranscriptionResponseExtensionSuffix
	EmbeddingRequestExtensionKey      = GoogleProviderID + EmbeddingRequestExtensionSuffix
	EmbeddingResponseExtensionKey     = GoogleProviderID + EmbeddingResponseExtensionSuffix
	ImageRequestExtensionKey          = GoogleProviderID + ImageRequestExtensionSuffix
	ImageResponseExtensionKey         = GoogleProviderID + ImageResponseExtensionSuffix
)

const (
	partReplayStateMetadataSuffix                 = "/part_state"
	usageMetadataSuffix                           = "/usage"
	toolUsePromptTokensMetadataSuffix             = "/tool_use_prompt_tokens"
	modelVersionMetadataSuffix                    = "/model_version"
	promptFeedbackMetadataSuffix                  = "/prompt_feedback"
	nativeFinishReasonMetadataSuffix              = "/native_finish_reason"
	safetyRatingsMetadataSuffix                   = "/safety_ratings"
	finishMessageMetadataSuffix                   = "/finish_message"
	speechMIMETypeMetadataSuffix                  = "/mime_type"
	embeddingTokenCountMetadataSuffix             = "/token_count"
	embeddingTruncatedMetadataSuffix              = "/truncated"
	embeddingBillableCharacterCountMetadataSuffix = "/billable_character_count"
	imageContentMetadataSuffix                    = "/image_content"
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
