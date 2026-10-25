package typesense

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Native rank counts every admitted hit, including hits below MinScore. Using
// the filtered result count as a cursor would change both pagination and fusion
// scores. The seen identities are the single source of the consumed rank.
type searchSession struct {
	schema  collectionSchema
	options vectorstore.SearchOptions
	query   searchQuery
	allowed map[string]struct{}
	seen    map[string]struct{}
	page    int
	total   *int
	results []*vectorstore.SearchResult
}

func newSearchSession(schema collectionSchema, options vectorstore.SearchOptions, query searchQuery, keys []string) *searchSession {
	session := &searchSession{
		schema: schema, options: options, query: query, page: 1,
		seen: make(map[string]struct{}),
	}
	if options.Filter != nil {
		session.allowed = make(map[string]struct{}, len(keys))
		for _, key := range keys {
			session.allowed[key] = struct{}{}
		}
		session.query.FilterBy = new(keyFilter(keys))
	}
	return session
}

func (s *searchSession) requestBody() ([]byte, error) {
	query := s.query
	query.Page = new(s.page)
	return jsonv2.Marshal(struct {
		Searches []searchQuery `json:"searches"`
	}{Searches: []searchQuery{query}})
}

func (s *searchSession) admitPage(raw []byte) (bool, error) {
	var wire struct {
		Results []searchPage `json:"results"`
	}
	if err := jsonv2.Unmarshal(raw, &wire); err != nil {
		return false, err
	}
	if len(wire.Results) != 1 {
		return false, errors.New("typesense: multi-search must return exactly one result")
	}
	page := wire.Results[0]
	if err := page.validate(); err != nil {
		return false, err
	}
	if s.total == nil {
		s.total = new(*page.Found)
	} else if *s.total != *page.Found {
		return false, errors.New("typesense: native result count changed during pagination")
	}
	expected := min(*s.total, s.options.ResultLimit())
	if len(*page.Hits) > *s.query.PerPage || len(*page.Hits) > expected-len(s.seen) {
		return false, errors.New("typesense: native search returned more hits than its page or result budget")
	}
	for _, hit := range *page.Hits {
		if err := s.admitHit(hit); err != nil {
			return false, err
		}
	}
	if len(s.seen) == expected {
		return true, nil
	}
	if len(*page.Hits) < *s.query.PerPage {
		return false, errors.New("typesense: native search ended before its reported result count")
	}
	s.page++
	return false, nil
}

func (s *searchSession) admitHit(hit searchHit) error {
	record, doc, err := s.schema.decodeDocument(hit.Document)
	if err != nil {
		return err
	}
	if _, duplicate := s.seen[*record.ID]; duplicate {
		return errors.New("typesense: search repeated a native key")
	}
	if membershipErr := s.validateMembership(*record.ID, doc); membershipErr != nil {
		return membershipErr
	}
	score, err := hit.score(s.options.EffectiveMode(), len(s.seen)+1)
	if err != nil {
		return err
	}
	result, err := vectorstore.NewSearchResult(doc, score)
	if err != nil {
		return err
	}
	s.seen[*record.ID] = struct{}{}
	if score >= s.options.MinScore {
		s.results = append(s.results, result)
	}
	return nil
}

func (s *searchSession) validateMembership(key string, doc *document.Document) error {
	if s.options.Filter == nil {
		return nil
	}
	if _, member := s.allowed[key]; !member {
		return errors.New("typesense: native hit is outside Core membership")
	}
	values, err := doc.Metadata.Values()
	if err != nil {
		return err
	}
	matched, err := filter.Match(s.options.Filter, values)
	if err != nil {
		return err
	}
	if !matched {
		return errors.New("typesense: native hit changed Core membership during search")
	}
	return nil
}

type searchPage struct {
	Code   *int         `json:"code"`
	Error  *string      `json:"error"`
	Found  *int         `json:"found"`
	Hits   *[]searchHit `json:"hits"`
	Cutoff *bool        `json:"search_cutoff"`
}

func (s searchPage) validate() error {
	if s.Error != nil || s.Code != nil && *s.Code >= http.StatusBadRequest {
		return fmt.Errorf("typesense: native search failed: code %d: %s", lo.FromPtr(s.Code), lo.FromPtr(s.Error))
	}
	if s.Found == nil || *s.Found < 0 || s.Hits == nil || s.Cutoff == nil || *s.Cutoff {
		return errors.New("typesense: native search omitted a complete result count, hit array or cutoff acknowledgment")
	}
	return nil
}

type searchHit struct {
	Document       json.RawMessage `json:"document"`
	VectorDistance *float64        `json:"vector_distance"`
}

func (s searchHit) score(mode vectorstore.SearchMode, rank int) (vectorstore.Score, error) {
	if s.VectorDistance != nil {
		distance := *s.VectorDistance
		if math.IsNaN(distance) || math.IsInf(distance, 0) || distance < 0 || distance > maxCosineDistance {
			return 0, errors.New("typesense: native cosine distance is outside [0,2]")
		}
		if mode == vectorstore.SearchModeSemantic {
			return vectorstore.ScoreFromCosineDistance(distance), nil
		}
	} else if mode == vectorstore.SearchModeSemantic {
		return 0, errors.New("typesense: semantic hit has no vector distance")
	}
	return vectorstore.Score(1 / float64(rank)), nil
}
