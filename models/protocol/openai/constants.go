package openai

// Namespacing preserves provider-specific data without promoting it into the
// shared Core protocol or colliding with another provider.
const (
	ProviderID                       = "openai"
	SpeechRequestExtensionKey        = ProviderID + "/speech_request"
	TranscriptionRequestExtensionKey = ProviderID + "/transcription_request"
	TranslationRequestExtensionKey   = ProviderID + "/translation_request"
	EmbeddingRequestExtensionKey     = ProviderID + "/embedding_request"
	ImageRequestExtensionKey         = ProviderID + "/image_request"
	ModerationRequestExtensionKey    = ProviderID + "/moderation_request"
)
