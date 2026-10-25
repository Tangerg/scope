package prodia

const (
	headerContentType         = "Content-Type"
	headerAccept              = "Accept"
	headerAuthorization       = "Authorization"
	authorizationBearerPrefix = "Bearer "
	mediaTypeJSON             = "application/json"
	mediaTypeBinary           = "application/octet-stream"
	mediaTypeImage            = "image/*"
	mediaTypeJPEG             = "image/jpeg"
	mediaTypePNG              = "image/png"
	mediaTypeWebP             = "image/webp"
	pathJob                   = "/job"
	textToImageJobSegment     = ".txt2img."
	metadataJobType           = "prodia/job_type"
	fieldType                 = "type"
	fieldPrompt               = "prompt"
	fieldNegativePrompt       = "negative_prompt"
	fieldWidth                = "width"
	fieldHeight               = "height"
	fieldSeed                 = "seed"
)
