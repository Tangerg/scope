package assemblyai

const (
	minimumContentSafetyConfidence = 25
	maximumContentSafetyConfidence = 100
	headerContentType              = "Content-Type"
	headerAuthorization            = "Authorization"
	mediaTypeJSON                  = "application/json"
	mediaTypeBinary                = "application/octet-stream"
	pathUpload                     = "/upload"
	pathTranscript                 = "/transcript"
	pathTranscriptPrefix           = "/transcript/"
	fieldAudioURL                  = "audio_url"
	fieldLanguageCode              = "language_code"
	metadataConfidence             = "assemblyai/confidence"
	metadataLanguageCode           = "assemblyai/language_code"
	metadataUtterances             = "assemblyai/utterances"
	metadataWords                  = "assemblyai/words"
	metadataTranscriptID           = "assemblyai/transcript_id"
	metadataDurationSeconds        = "assemblyai/audio_duration_seconds"
)
