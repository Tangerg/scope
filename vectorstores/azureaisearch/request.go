package azureaisearch

const (
	searchActionField = "@search.action"
	searchScoreField  = "@search.score"
	// searchAnnotationPrefix marks every member the service adds to a result,
	// so none of them is a stored field.
	searchAnnotationPrefix = "@"
	queryKindVector        = "vector"
)

type searchRequest struct {
	Count         *bool         `json:"count,omitzero"`
	Top           int           `json:"top"`
	Select        string        `json:"select"`
	VectorQueries []vectorQuery `json:"vectorQueries,omitzero"`
	Search        string        `json:"search,omitzero"`
	SearchFields  string        `json:"searchFields,omitzero"`
	OrderBy       string        `json:"orderby,omitzero"`
	Filter        string        `json:"filter,omitzero"`
}

type vectorQuery struct {
	Kind   string    `json:"kind"`
	Vector []float32 `json:"vector"`
	K      int       `json:"k"`
	Fields string    `json:"fields"`
}

type indexingRequest struct {
	Value []map[string]any `json:"value"`
}
