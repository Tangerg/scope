package typesense

const (
	fieldID        = "id"
	fieldContent   = "content"
	fieldMetadata  = "metadata"
	fieldEmbedding = "embedding"

	fieldTypeString     = "string"
	fieldTypeFloatArray = "float[]"
	distanceCosine      = "cosine"
	maxCosineDistance   = 2

	wildcardQuery  = "*"
	mediaTypeJSON  = "application/json"
	mediaTypeJSONL = "application/octet-stream"

	vectorQueryStart     = ":(["
	vectorQueryNeighbors = "], k: "
	vectorQueryAlpha     = ", alpha: "
	keyFilterOperator    = ":=["
)
