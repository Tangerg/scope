package typesense

const (
	fieldID        = "id"
	fieldContent   = "content"
	fieldMetadata  = "metadata"
	fieldEmbedding = "embedding"

	fieldTypeString     = "string"
	fieldTypeFloatArray = "float[]"
	distanceCosine      = "cosine"

	wildcardQuery = "*"
	mediaTypeJSON = "application/json"
	// importMediaType labels the JSONL import body as the Typesense client does.
	importMediaType = "application/octet-stream"

	vectorQueryStart     = ":(["
	vectorQueryNeighbors = "], k: "
	vectorQueryAlpha     = ", alpha: "
	keyFilterOperator    = ":=["
)
