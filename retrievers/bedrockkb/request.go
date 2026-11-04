package bedrockkb

type retrievalRequest struct {
	RetrievalQuery         retrievalQuery         `json:"retrievalQuery"`
	RetrievalConfiguration retrievalConfiguration `json:"retrievalConfiguration"`
	UserContext            *retrievalUserContext  `json:"userContext,omitzero"`
	NextToken              string                 `json:"nextToken,omitzero"`
}

type retrievalQuery struct {
	Text string `json:"text"`
}

type retrievalConfiguration struct {
	VectorSearchConfiguration nativeConfiguration `json:"vectorSearchConfiguration"`
}

type retrievalUserContext struct {
	UserID string `json:"userId"`
}
