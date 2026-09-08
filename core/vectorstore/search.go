package vectorstore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Relevance-score range for [SearchOptions.MinScore] and search defaults.
const (
	// DefaultTopK is used when [SearchOptions.TopK] is zero.
	DefaultTopK = 5

	// MinRelevanceScore is the lowest valid score.
	MinRelevanceScore = 0.0

	// MaxRelevanceScore is the highest valid score.
	MaxRelevanceScore = 1.0
)

// SearchMode selects the retrieval evidence used by a search operation.
type SearchMode string

// Search modes are explicit so a caller can require one and be refused by a
// store that cannot honor it. Silently downgrading a hybrid request to
// semantic would return plausible results that answer a different query than
// the one asked.
const (
	SearchModeSemantic SearchMode = "semantic"
	SearchModeHybrid   SearchMode = "hybrid"
)

func (s SearchMode) Valid() bool {
	switch s {
	case "", SearchModeSemantic, SearchModeHybrid:
		return true
	default:
		return false
	}
}

func (s SearchMode) String() string {
	if s == "" {
		return string(SearchModeSemantic)
	}
	return string(s)
}

// SearchOptions owns the policies applied to a relevance search. Semantic is
// the zero-value mode; hybrid combines semantic and lexical evidence.
type SearchOptions struct {
	// TopK limits the result count. Zero uses DefaultTopK.
	TopK     int   `json:"top_k,omitempty"`
	MinScore Score `json:"min_score,omitempty"`
	// Filter is encoded as its canonical filter DSL string. An omitted or null
	// JSON filter means no predicate; a present string must parse successfully.
	Filter filter.Predicate `json:"filter,omitempty"`
	Mode   SearchMode       `json:"mode,omitempty"`
}

type searchOptionsWire struct {
	TopK     int        `json:"top_k,omitempty"`
	MinScore Score      `json:"min_score,omitempty"`
	Filter   *string    `json:"filter,omitempty"`
	Mode     SearchMode `json:"mode,omitempty"`
}

func (s SearchOptions) Validate() error {
	if s.TopK < 0 {
		return fmt.Errorf("%w: top K must not be negative, got %d", ErrInvalidOptions, s.TopK)
	}
	if err := s.MinScore.Validate(); err != nil {
		return fmt.Errorf("%w: minimum score: %w", ErrInvalidOptions, err)
	}
	if !s.Mode.Valid() {
		return fmt.Errorf("%w: unknown search mode %q", ErrInvalidOptions, s.Mode)
	}
	if s.EffectiveMode() == SearchModeHybrid && s.MinScore != MinRelevanceScore {
		return fmt.Errorf("%w: minimum score is not portable across hybrid fusion algorithms", ErrInvalidOptions)
	}
	if s.Filter != nil {
		if err := s.Filter.Validate(); err != nil {
			return fmt.Errorf("%w: filter: %w", ErrInvalidOptions, err)
		}
	}
	return nil
}

// EffectiveMode returns semantic for the zero-value mode.
func (s SearchOptions) EffectiveMode() SearchMode {
	if s.Mode == "" {
		return SearchModeSemantic
	}
	return s.Mode
}

// RequireMode rejects a valid request mode before provider I/O when the store
// cannot implement it without changing its semantics.
func (s SearchOptions) RequireMode(supported ...SearchMode) error {
	if err := s.Validate(); err != nil {
		return err
	}
	mode := s.EffectiveMode()
	if slices.Contains(supported, mode) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnsupportedSearchMode, mode)
}

// ResultLimit returns the explicit TopK or DefaultTopK when it is omitted.
func (s SearchOptions) ResultLimit() int {
	if s.TopK == 0 {
		return DefaultTopK
	}
	return s.TopK
}

func (s SearchOptions) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	wire := searchOptionsWire{TopK: s.TopK, MinScore: s.MinScore, Mode: s.Mode}
	if s.Filter != nil {
		wire.Filter = new(s.Filter.String())
	}
	return json.Marshal(wire)
}

func (s *SearchOptions) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: search options receiver is nil", ErrInvalidOptions)
	}
	var decoded searchOptionsWire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: decode search options: %w", ErrInvalidOptions, err)
	}
	candidate := SearchOptions{TopK: decoded.TopK, MinScore: decoded.MinScore, Mode: decoded.Mode}
	if decoded.Filter != nil {
		predicate, err := filter.Parse(*decoded.Filter)
		if err != nil {
			return fmt.Errorf("%w: filter: %w", ErrInvalidOptions, err)
		}
		candidate.Filter = predicate
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	*s = candidate
	return nil
}

// SearchRequest describes one relevance search and owns its input validation.
type SearchRequest struct {
	Query   string        `json:"query,omitempty"`
	Options SearchOptions `json:"options"`
}

// NewSearchRequest starts from the query alone because everything else — top-k,
// filters, mode — has a defined default. Requiring them would push the same
// boilerplate into every call site.
func NewSearchRequest(query string) (*SearchRequest, error) {
	request := &SearchRequest{Query: query}
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("vectorstore: create search request: %w", err)
	}
	return request, nil
}

func (s *SearchRequest) Validate() error {
	if s == nil {
		return fmt.Errorf("%w: search request is nil", ErrInvalidRequest)
	}
	if strings.TrimSpace(s.Query) == "" {
		return fmt.Errorf("%w: query must not be empty", ErrInvalidRequest)
	}
	if err := s.Options.Validate(); err != nil {
		return fmt.Errorf("%w: options: %w", ErrInvalidRequest, err)
	}
	return nil
}

func (s SearchRequest) MarshalJSON() ([]byte, error) {
	if err := (&s).Validate(); err != nil {
		return nil, err
	}
	type wireSearchRequest SearchRequest
	return json.Marshal(wireSearchRequest(s))
}

func (s *SearchRequest) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: search request receiver is nil", ErrInvalidRequest)
	}
	type wireSearchRequest SearchRequest
	var decoded wireSearchRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: decode search request: %w", ErrInvalidRequest, err)
	}
	candidate := SearchRequest(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*s = candidate
	return nil
}

// SearchResult relates a document to one search operation. Score is deliberately
// kept outside document.Document: relevance belongs to a query/result pair,
// not to the indexed content itself.
type SearchResult struct {
	Document *document.Document `json:"document"`
	Score    Score              `json:"score"`
}

// NewSearchResult pairs a document with a validated [Score], so a backend
// cannot emit a NaN or out-of-range relevance that would silently corrupt
// ranking and fusion downstream.
func NewSearchResult(matched *document.Document, score Score) (*SearchResult, error) {
	result := &SearchResult{Document: matched, Score: score}
	if err := result.Validate(); err != nil {
		return nil, fmt.Errorf("vectorstore: create search result: %w", err)
	}
	return result, nil
}

func (s *SearchResult) Validate() error {
	if s == nil {
		return fmt.Errorf("%w: result is nil", ErrInvalidResponse)
	}
	if s.Document == nil {
		return fmt.Errorf("%w: result document is nil", ErrInvalidResponse)
	}
	if err := s.Document.Validate(); err != nil {
		return fmt.Errorf("%w: result document: %w", ErrInvalidResponse, err)
	}
	if strings.TrimSpace(s.Document.ID) == "" {
		return fmt.Errorf("%w: result: %w", ErrInvalidResponse, ErrMissingDocumentID)
	}
	if err := s.Score.Validate(); err != nil {
		return fmt.Errorf("%w: result score: %w", ErrInvalidResponse, err)
	}
	return nil
}

func (s SearchResult) MarshalJSON() ([]byte, error) {
	if err := (&s).Validate(); err != nil {
		return nil, err
	}
	type wireSearchResult SearchResult
	return json.Marshal(wireSearchResult(s))
}

func (s *SearchResult) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: search result receiver is nil", ErrInvalidResponse)
	}
	type wireSearchResult SearchResult
	var decoded wireSearchResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: decode search result: %w", ErrInvalidResponse, err)
	}
	candidate := SearchResult(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*s = candidate
	return nil
}

// SearchResponse owns a complete ranked result set.
type SearchResponse struct {
	Results []*SearchResult `json:"results"`
}

// NewSearchResponse validates the result set as a whole, which is where an
// adapter's ordering or scoring mistakes become visible; an individually valid
// result says nothing about the ranking it sits in.
func NewSearchResponse(results []*SearchResult) (*SearchResponse, error) {
	response := &SearchResponse{Results: slices.Clone(results)}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("vectorstore: create search response: %w", err)
	}
	return response, nil
}

func (s *SearchResponse) Validate() error {
	if s == nil {
		return fmt.Errorf("%w: response is nil", ErrInvalidResponse)
	}
	for index, result := range s.Results {
		if err := result.Validate(); err != nil {
			return fmt.Errorf("%w: results[%d]: %w", ErrInvalidResponse, index, err)
		}
		if index > 0 && s.Results[index-1].Score < result.Score {
			return fmt.Errorf("%w: results are not sorted by descending score at index %d", ErrInvalidResponse, index)
		}
	}
	return nil
}

func (s SearchResponse) MarshalJSON() ([]byte, error) {
	if err := (&s).Validate(); err != nil {
		return nil, err
	}
	type wireSearchResponse SearchResponse
	return json.Marshal(wireSearchResponse(s))
}

func (s *SearchResponse) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: search response receiver is nil", ErrInvalidResponse)
	}
	type wireSearchResponse SearchResponse
	var decoded wireSearchResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: decode search response: %w", ErrInvalidResponse, err)
	}
	candidate := SearchResponse(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*s = candidate
	return nil
}

func (s *SearchResponse) ValidateFor(request *SearchRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := s.Validate(); err != nil {
		return err
	}
	limit := request.Options.ResultLimit()
	if len(s.Results) > limit {
		return fmt.Errorf("%w: got %d results, top K is %d", ErrInvalidResponse, len(s.Results), limit)
	}
	for index, result := range s.Results {
		if result.Score < request.Options.MinScore {
			return fmt.Errorf("%w: results[%d] score %v is below minimum score %v",
				ErrInvalidResponse, index, result.Score, request.Options.MinScore)
		}
	}
	return nil
}

func (s *SearchResponse) First() *SearchResult {
	if s == nil || len(s.Results) == 0 {
		return nil
	}
	return s.Results[0]
}

func (s *SearchResponse) Documents() []*document.Document {
	if s == nil {
		return nil
	}
	documents := make([]*document.Document, len(s.Results))
	for i, result := range s.Results {
		if result != nil {
			documents[i] = result.Document
		}
	}
	return documents
}

// Searcher retrieves documents ranked by query relevance in descending order.
type Searcher interface {
	// Search returns a response honoring the mode, semantic score threshold,
	// metadata filter, and result cap owned by [SearchRequest.Options].
	Search(ctx context.Context, request *SearchRequest) (*SearchResponse, error)
}
