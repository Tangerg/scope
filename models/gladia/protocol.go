package gladia

const (
	headerAPIKey            = "x-gladia-key"
	headerContentType       = "Content-Type"
	mediaTypeJSON           = "application/json"
	mediaTypeBinary         = "application/octet-stream"
	multipartAudio          = "audio"
	pathUpload              = "/upload"
	pathTranscription       = "/pre-recorded"
	pathTranscriptionPrefix = "/pre-recorded/"
	fieldAudioURL           = "audio_url"
	fieldModel              = "model"
	fieldLanguageConfig     = "language_config"
	fieldLanguages          = "languages"
	metadataLanguages       = "gladia/languages"
	metadataUtterances      = "gladia/utterances"
	metadataTranslation     = "gladia/translation"
	metadataSummarization   = "gladia/summarization"
	metadataTranscriptID    = "gladia/transcript_id"
)
