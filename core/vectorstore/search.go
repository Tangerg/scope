package vectorstore

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const (
	DefaultTopK = 5

	MinRelevanceScore = 0.0

	MaxRelevanceScore = 1.0
)

type SearchMode string

// Stores must reject unsupported search modes rather than silently substitute another.
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
	TopK     int   `json:"top_k,omitzero"`
	MinScore Score `json:"min_score,omitzero"`
	// Filter is encoded as its canonical filter DSL string. An omitted or null
	// JSON filter means no predicate; a present string must parse successfully.
	Filter filter.Predicate `json:"filter,omitempty"`
	Mode   SearchMode       `json:"mode,omitempty"`
}

type searchOptionsWire struct {
	TopK     int        `json:"top_k,omitzero"`
	MinScore Score      `json:"min_score,omitzero"`
	Filter   *string    `json:"filter,omitzero"`
	Mode     SearchMode `json:"mode,omitempty"`
}

func (s SearchOptions) Clone() SearchOptions {
	s.Filter = filter.Clone(s.Filter)
	return s
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

func (s SearchOptions) EffectiveMode() SearchMode {
	return SearchMode(s.Mode.String())
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
	return jsonv2.Marshal(wire)
}

func (s *SearchOptions) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: search options receiver is nil", ErrInvalidOptions)
	}
	var decoded searchOptionsWire
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
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

type SearchResult struct {
	Document *document.Document `json:"document"`
	Score    Score              `json:"score"`
}

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
	// Reads and writes use the same document and identity contract.
	if s.Document == nil {
		return fmt.Errorf("%w: missing document", ErrInvalidResponse)
	}
	if err := validatePresentDocument(s.Document); err != nil {
		return fmt.Errorf("%w: result: %w", ErrInvalidResponse, err)
	}
	if err := s.Score.Validate(); err != nil {
		return fmt.Errorf("%w: result score: %w", ErrInvalidResponse, err)
	}
	return nil
}

func (s *SearchResult) validateFilter(predicate filter.Predicate) error {
	if predicate == nil {
		return nil
	}
	values, err := s.Document.Metadata.Values()
	if err != nil {
		return fmt.Errorf("decode filter metadata: %w", err)
	}
	matches, err := filter.Match(predicate, values)
	if err != nil {
		return fmt.Errorf("evaluate metadata filter: %w", err)
	}
	if !matches {
		return errors.New("document does not match the requested metadata filter")
	}
	return nil
}

func (s SearchResult) MarshalJSON() ([]byte, error) {
	if err := (&s).Validate(); err != nil {
		return nil, err
	}
	type wireSearchResult SearchResult
	return jsonv2.Marshal(wireSearchResult(s))
}

func (s *SearchResult) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: search result receiver is nil", ErrInvalidResponse)
	}
	var decoded struct {
		Document *document.Document `json:"document"`
		Score    *Score             `json:"score"`
	}
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode search result: %w", ErrInvalidResponse, err)
	}
	if decoded.Score == nil {
		return fmt.Errorf("%w: search result requires score", ErrInvalidResponse)
	}
	candidate := SearchResult{Document: decoded.Document, Score: *decoded.Score}
	if err := candidate.Validate(); err != nil {
		return err
	}
	*s = candidate
	return nil
}

// SearchResponse reports a completed search. JSON requires a results array;
// an explicit empty array reports no matches.
type SearchResponse struct {
	Results []*SearchResult `json:"results"`
}

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
	return jsonv2.Marshal(wireSearchResponse(s))
}

func (s *SearchResponse) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("%w: search response receiver is nil", ErrInvalidResponse)
	}
	type wireSearchResponse SearchResponse
	var decoded wireSearchResponse
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode search response: %w", ErrInvalidResponse, err)
	}
	if decoded.Results == nil {
		return fmt.Errorf("%w: search response requires results", ErrInvalidResponse)
	}
	candidate := SearchResponse(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*s = candidate
	return nil
}

func (s *SearchResponse) validateOptions(options SearchOptions) error {
	if err := s.Validate(); err != nil {
		return err
	}
	limit := options.ResultLimit()
	if len(s.Results) > limit {
		return fmt.Errorf("%w: got %d results, top K is %d", ErrInvalidResponse, len(s.Results), limit)
	}
	for index, result := range s.Results {
		if result.Score < options.MinScore {
			return fmt.Errorf("%w: results[%d] score %v is below minimum score %v",
				ErrInvalidResponse, index, result.Score, options.MinScore)
		}
		if err := result.validateFilter(options.Filter); err != nil {
			return fmt.Errorf("%w: results[%d]: %w", ErrInvalidResponse, index, err)
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
