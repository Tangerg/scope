package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
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

	returnFields := []goredis.FTSearchReturn{
		{FieldName: s.contentField},
		{FieldName: distanceFieldName},
	}
	for _, f := range s.metadataFields {
		returnFields = append(returnFields, goredis.FTSearchReturn{FieldName: f.Name})
	}

	opts := &goredis.FTSearchOptions{
		Params: map[string]any{
			vectorParamName: queryVec,
		},
		Return:         returnFields,
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

	if len(s.metadataFields) > 0 {
		meta := make(map[string]any, len(s.metadataFields))
		for _, f := range s.metadataFields {
			if v, ok := hit.Fields[f.Name]; ok {
				meta[f.Name] = parseMetadataValue(v, f.Type)
			}
		}
		if len(meta) > 0 {
			var err error
			doc.Metadata, err = metadata.FromValues(meta)
			if err != nil {
				return nil, fmt.Errorf("redis: convert metadata: %w", err)
			}
		}
	}
	return doc, nil
}

// parseMetadataValue reverses formatMetadataValue based on the schema
// type — numeric fields come back as float64, everything else stays
// a string.
func parseMetadataValue(raw string, t MetadataFieldType) any {
	if t == FieldNumeric {
		if n, err := strconv.ParseFloat(raw, 64); err == nil {
			return n
		}
	}
	return raw
}
