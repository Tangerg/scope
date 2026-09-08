package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Search embeds the query, runs a KNN search through RediSearch,
// and returns the matching documents above MinScore.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("redis.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("redis.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("redis: embed query: %w", err)
	}
	queryVec := float32sToBytes(embedding.Float32Vector(vector))

	filterQuery, err := s.buildFilterQuery(req.Options.Filter)
	if err != nil {
		return nil, err
	}

	// RediSearch hybrid syntax: <filter>=>[KNN <k> @embedding $vec AS distance]
	queryStr := fmt.Sprintf(
		"%s=>[KNN %d @%s $%s AS %s]",
		filterQuery, req.Options.ResultLimit(), s.embeddingField, vectorParamName, distanceFieldName,
	)

	opts := &goredis.FTSearchOptions{
		Params: map[string]any{
			vectorParamName: queryVec,
		},
		Return:         s.returnFields(),
		LimitOffset:    0,
		Limit:          req.Options.ResultLimit(),
		DialectVersion: redisSearchDialectVersion,
		SortBy: []goredis.FTSearchSortBy{
			{FieldName: distanceFieldName, Asc: true},
		},
	}

	result, err := s.client.FTSearchWithArgs(ctx, s.indexName, queryStr, opts).Result()
	if err != nil {
		return nil, fmt.Errorf("redis: FT.SEARCH %s: %w", s.indexName, err)
	}
	if err := checkSearchCompleteness(s.indexName, result); err != nil {
		return nil, err
	}

	docs = make([]*vectorstore.SearchResult, 0, len(result.Docs))
	for _, hit := range result.Docs {
		score, err := s.scoreFromFields(hit.Fields)
		if err != nil {
			return nil, err
		}
		if score < req.Options.MinScore {
			continue
		}
		doc, err := s.toDocument(hit)
		if err != nil {
			return nil, err
		}
		docs = append(docs, &vectorstore.SearchResult{Document: doc, Score: score})
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

// returnFields is everything a search result is read from. RETURN limits the
// reply to the fields it lists, so a field missing here reads back as an absent
// field rather than as an error — which is why the list is named and pinned
// rather than assembled at the call site.
//
// The declared metadata fields are absent on purpose: they exist so RediSearch
// can index and filter on them, and the metadata field is what a result reads
// its metadata back from, so the projection never has to come over the wire.
func (s *Store) returnFields() []goredis.FTSearchReturn {
	return []goredis.FTSearchReturn{
		{FieldName: s.contentField},
		{FieldName: distanceFieldName},
		{FieldName: s.metadataJSONField},
	}
}

func (s *Store) scoreFromFields(fields map[string]string) (vectorstore.Score, error) {
	raw, ok := fields[distanceFieldName]
	if !ok {
		return 0, fmt.Errorf("redis: missing distance field %q in result", distanceFieldName)
	}
	dist, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("redis: parse distance %q: %w", raw, err)
	}
	return s.distanceMetric.score(dist), nil
}

func (s *Store) toDocument(hit goredis.Document) (*document.Document, error) {
	id := strings.TrimPrefix(hit.ID, s.keyPrefix)
	if id == "" {
		return nil, errors.New("redis: search result is missing document ID")
	}
	text := hit.Fields[s.contentField]
	if text == "" {
		return nil, fmt.Errorf("redis: document %q is missing field %q", id, s.contentField)
	}
	doc := &document.Document{
		ID:   id,
		Text: text,
	}

	// Metadata comes from the JSON field rather than from the declared index
	// fields. A declared field holds the value in the form its RediSearch type
	// expects, so reading it back turned a number into a float64 and everything
	// else into a string, and an undeclared key had no field to read at all —
	// a search returned a document that differed from the one that was written.
	if raw, ok := hit.Fields[s.metadataJSONField]; ok && raw != "" {
		if err := json.Unmarshal([]byte(raw), &doc.Metadata); err != nil {
			return nil, fmt.Errorf("redis: decode metadata for %q: %w", id, err)
		}
	}
	return doc, nil
}
