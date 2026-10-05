package mongodb

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

type Store struct {
	collection      DocumentCollection
	vectorIndexName string
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	schema          nativeSchema
	numCandidates   int
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config.applyDefaults()
	model, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	store := &Store{collection: config.Collection, vectorIndexName: config.VectorIndexName, embeddingClient: model, documentBatcher: config.DocumentBatcher, numCandidates: config.NumCandidates}
	if err = store.bind(ctx); err != nil {
		return nil, err
	}
	if _, err = store.selectDocuments(ctx, nil); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) bind(ctx context.Context) (err error) {
	cursor, err := s.collection.Aggregate(ctx, mongo.Pipeline{{{Key: "$listSearchIndexes", Value: bson.M{"name": s.vectorIndexName}}}})
	if err != nil {
		return err
	}
	if cursor == nil {
		return errors.New("mongodb: native index listing returned no cursor")
	}
	defer func() { err = errors.Join(err, cursor.Close(ctx)) }()
	found := false
	for cursor.Next(ctx) {
		if found {
			return errors.New("mongodb: native index listing repeated the named index")
		}
		if err = s.schema.read(cursor.Current, s.vectorIndexName); err != nil {
			return err
		}
		found = true
	}
	if err = cursor.Err(); err != nil {
		return err
	}
	if !found {
		return errors.New("mongodb: native vector index is missing")
	}
	return nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	records := make(map[string]bson.M, len(request.Documents))
	for _, doc := range request.Documents {
		record, err := encodeRecord(doc)
		if err != nil {
			return err
		}
		records[doc.ID] = record
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	var writes []mongo.WriteModel
	for _, batch := range batches {
		texts, textErr := batch.Texts()
		if textErr != nil {
			return textErr
		}
		vectors, modelErr := s.embeddingClient.EmbedTexts(ctx, texts)
		if modelErr != nil {
			return modelErr
		}
		for i, doc := range batch.Documents {
			vector, vectorErr := s.schema.vector(vectors[i])
			if vectorErr != nil {
				return vectorErr
			}
			record := records[doc.ID]
			record[embeddingField] = vector
			raw, encodingErr := bson.Marshal(record)
			if encodingErr != nil {
				return encodingErr
			}
			writes = append(writes, mongo.NewReplaceOneModel().SetFilter(bson.M{idField: doc.ID}).SetReplacement(bson.Raw(raw)).SetUpsert(true).SetCollation(&options.Collation{Locale: "simple"}))
		}
	}
	result, err := s.collection.BulkWrite(ctx, writes)
	if err != nil {
		return err
	}
	return checkBulkAcknowledgment(result, len(writes))
}

func checkBulkAcknowledgment(result *mongo.BulkWriteResult, sent int) error {
	if result == nil {
		return errors.New("mongodb: bulk write returned no result")
	}
	if !result.Acknowledged {
		return errors.New("mongodb: collection writes are unacknowledged (w: 0), so an upsert cannot be confirmed")
	}
	if result.MatchedCount < 0 || result.UpsertedCount < 0 || result.MatchedCount+result.UpsertedCount != int64(sent) {
		return fmt.Errorf("mongodb: bulk write applied %d of %d documents", result.MatchedCount+result.UpsertedCount, sent)
	}
	return nil
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
		if err != nil {
			response = nil
		}
	}()
	candidates, err := s.searchCandidates(request.Options.ResultLimit())
	if err != nil {
		return nil, err
	}
	selected, err := s.selectDocuments(ctx, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return &vectorstore.SearchResponse{}, nil
	}
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, err
	}
	query, err := s.schema.vector(vector)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, record := range selected {
		ids = append(ids, record.id)
	}
	groups := [][]string{nil}
	if request.Options.Filter != nil {
		groups = slices.Collect(slices.Chunk(ids, filterPageSize))
	}
	var matches []*vectorstore.SearchResult
	seen := make(map[string]struct{})
	for _, group := range groups {
		native := bson.M{"index": s.vectorIndexName, "path": embeddingField, "queryVector": query, "numCandidates": candidates, "limit": request.Options.ResultLimit()}
		if group != nil {
			native["filter"] = bson.M{idField: bson.M{"$in": group}}
		}
		pipeline := mongo.Pipeline{{{Key: "$vectorSearch", Value: native}}, {{Key: "$addFields", Value: bson.M{scoreField: bson.M{"$meta": "vectorSearchScore"}}}}}
		hits, queryErr := s.queryMatches(ctx, pipeline)
		if queryErr != nil {
			return nil, queryErr
		}
		if len(hits) > request.Options.ResultLimit() {
			return nil, errors.New("mongodb: native query exceeded its result limit")
		}
		for _, hit := range hits {
			id := hit.Document.ID
			if _, duplicate := seen[id]; duplicate {
				return nil, errors.New("mongodb: native query repeated an identity")
			}
			seen[id] = struct{}{}
			if group != nil {
				if !slices.Contains(group, id) {
					return nil, errors.New("mongodb: hit is outside selected identities")
				}
				values, decodeErr := hit.Document.Metadata.Values()
				if decodeErr != nil {
					return nil, decodeErr
				}
				matched, matchErr := filter.Match(request.Options.Filter, values)
				if matchErr != nil {
					return nil, matchErr
				}
				if !matched {
					return nil, errors.New("mongodb: native hit changed Core membership")
				}
			}
			matches = append(matches, hit)
		}
	}
	slices.SortFunc(matches, func(left, right *vectorstore.SearchResult) int {
		if order := cmp.Compare(right.Score, left.Score); order != 0 {
			return order
		}
		return strings.Compare(left.Document.ID, right.Document.ID)
	})
	matches = matches[:min(len(matches), request.Options.ResultLimit())]
	response = &vectorstore.SearchResponse{}
	for _, hit := range matches {
		if hit.Score >= request.Options.MinScore {
			response.Results = append(response.Results, hit)
		}
	}
	return response, nil
}

func (s *Store) queryMatches(ctx context.Context, pipeline mongo.Pipeline) (matches []*vectorstore.SearchResult, err error) {
	cursor, err := s.collection.Aggregate(ctx, pipeline, options.Aggregate().SetCollation(&options.Collation{Locale: "simple"}))
	if err != nil {
		return nil, err
	}
	if cursor == nil {
		return nil, errors.New("mongodb: native query returned no cursor")
	}
	defer func() {
		err = errors.Join(err, cursor.Close(ctx))
		if err != nil {
			matches = nil
		}
	}()
	for cursor.Next(ctx) {
		doc, _, decodeErr := s.schema.decode(cursor.Current, true)
		if decodeErr != nil {
			return nil, decodeErr
		}
		raw, ok := cursor.Current.Lookup(scoreField).DoubleOK()
		if !ok {
			return nil, errors.New("mongodb: native score must be a double")
		}
		result, resultErr := vectorstore.NewSearchResult(doc, vectorstore.Score(raw))
		if resultErr != nil {
			return nil, resultErr
		}
		matches = append(matches, result)
	}
	return matches, cursor.Err()
}

func (s *Store) selectDocuments(ctx context.Context, predicate filter.Predicate) (selected []selectedDocument, err error) {
	cursor, err := s.collection.Aggregate(ctx, mongo.Pipeline{}, options.Aggregate().SetCollation(&options.Collation{Locale: "simple"}))
	if err != nil {
		return nil, err
	}
	if cursor == nil {
		return nil, errors.New("mongodb: native enumeration returned no cursor")
	}
	defer func() {
		err = errors.Join(err, cursor.Close(ctx))
		if err != nil {
			selected = nil
		}
	}()
	seen := make(map[string]struct{})
	for cursor.Next(ctx) {
		doc, facts, decodeErr := s.schema.decode(cursor.Current, false)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if _, duplicate := seen[doc.ID]; duplicate {
			return nil, errors.New("mongodb: native enumeration repeated an identity")
		}
		seen[doc.ID] = struct{}{}
		matched := true
		if predicate != nil {
			values, valuesErr := doc.Metadata.Values()
			if valuesErr != nil {
				return nil, valuesErr
			}
			matched, err = filter.Match(predicate, values)
			if err != nil {
				return nil, err
			}
		}
		if matched {
			selected = append(selected, selectedDocument{id: doc.ID, metadataJSON: facts})
		}
	}
	return selected, cursor.Err()
}

func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	records, err := s.selectDocuments(ctx, predicate)
	if err != nil {
		return err
	}
	for _, record := range records {
		constraint := bson.M{idField: record.id, "$expr": bson.M{"$eq": bson.A{"$" + metadataField, bson.M{"$literal": record.metadataJSON}}}}
		result, deleteErr := s.collection.DeleteMany(ctx, constraint, options.DeleteMany().SetCollation(&options.Collation{Locale: "simple"}))
		if deleteErr != nil {
			return deleteErr
		}
		if result == nil || !result.Acknowledged || result.DeletedCount < 0 || result.DeletedCount > 1 {
			return errors.New("mongodb: conditional deletion was not acknowledged with a valid count")
		}
	}
	return nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	var selected []string
	seen := make(map[string]struct{})
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || !utf8.ValidString(id) {
			return vectorstore.ErrMissingDocumentID
		}
		if _, duplicate := seen[id]; !duplicate {
			seen[id] = struct{}{}
			selected = append(selected, id)
		}
	}
	if len(selected) == 0 {
		return nil
	}
	result, err := s.collection.DeleteMany(ctx, bson.M{idField: bson.M{"$in": selected}}, options.DeleteMany().SetCollation(&options.Collation{Locale: "simple"}))
	if err != nil {
		return err
	}
	if result == nil || !result.Acknowledged || result.DeletedCount < 0 || result.DeletedCount > int64(len(selected)) {
		return errors.New("mongodb: deletion was not acknowledged with a valid count")
	}
	return nil
}

func (s *Store) searchCandidates(limit int) (int, error) {
	if limit > MaxNumCandidates {
		return 0, fmt.Errorf("mongodb.Store.Search: TopK %d exceeds the %d candidates $vectorSearch can consider", limit, MaxNumCandidates)
	}
	return max(s.numCandidates, limit), nil
}
