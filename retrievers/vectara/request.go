package vectara

const semanticLexicalInterpolation = 0

type queryRequest struct {
	Query                     string      `json:"query"`
	Search                    querySearch `json:"search"`
	StreamResponse            bool        `json:"stream_response"`
	SaveHistory               bool        `json:"save_history"`
	IntelligentQueryRewriting bool        `json:"intelligent_query_rewriting"`
}

type querySearch struct {
	Limit                int           `json:"limit"`
	LexicalInterpolation float64       `json:"lexical_interpolation"`
	Reranker             queryReranker `json:"reranker"`
	MetadataFilter       string        `json:"metadata_filter,omitzero"`
}

type queryReranker struct {
	Type string `json:"type"`
}
