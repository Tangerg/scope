package openai

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider. Every provider bound
// to this protocol keys a modality's native request fields by its own ID and
// the modality's suffix.
const (
	ProviderID = "openai"

	SpeechRequestExtensionSuffix        = "/speech_request"
	TranscriptionRequestExtensionSuffix = "/transcription_request"
	TranslationRequestExtensionSuffix   = "/translation_request"
	EmbeddingRequestExtensionSuffix     = "/embedding_request"
	ImageRequestExtensionSuffix         = "/image_request"
	ModerationRequestExtensionSuffix    = "/moderation_request"
	// ImageRevisedPromptMetadataSuffix keys an image output's prompt as the
	// provider revised it before generating.
	ImageRevisedPromptMetadataSuffix = "/revised_prompt"

	SpeechRequestExtensionKey        = ProviderID + SpeechRequestExtensionSuffix
	TranscriptionRequestExtensionKey = ProviderID + TranscriptionRequestExtensionSuffix
	TranslationRequestExtensionKey   = ProviderID + TranslationRequestExtensionSuffix
	EmbeddingRequestExtensionKey     = ProviderID + EmbeddingRequestExtensionSuffix
	ImageRequestExtensionKey         = ProviderID + ImageRequestExtensionSuffix
	ModerationRequestExtensionKey    = ProviderID + ModerationRequestExtensionSuffix
)
