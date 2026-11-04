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

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/filter/keyword"
)

var (
	_ vectorstore.Upserter      = (*Store)(nil)
	_ vectorstore.Querier       = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

type Store struct {
	collection      DocumentCollection
	vectorIndexName string
	space           vectorstore.SpaceID
	schema          nativeSchema
	numCandidates   int
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config.applyDefaults()
	var err error
	store := &Store{collection: config.Collection, vectorIndexName: config.VectorIndexName, space: config.Space, numCandidates: config.NumCandidates}
	if store.schema, err = store.readSchema(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) readSchema(ctx context.Context) (schema nativeSchema, err error) {
	cursor, err := s.collection.Aggregate(ctx, mongo.Pipeline{{{Key: "$listSearchIndexes", Value: bson.M{"name": s.vectorIndexName}}}})
	if err != nil {
		return schema, err
	}
	if cursor == nil {
		return schema, errors.New("mongodb: native index listing returned no cursor")
	}
	defer func() { err = errors.Join(err, cursor.Close(ctx)) }()
	found := false
	for cursor.Next(ctx) {
		if found {
			return schema, errors.New("mongodb: native index listing repeated the named index")
		}
		if err = schema.read(cursor.Current, s.vectorIndexName, s.space); err != nil {
			return schema, err
		}
		found = true
	}
	if err = cursor.Err(); err != nil {
		return schema, err
	}
	if !found {
		return schema, errors.New("mongodb: native vector index is missing")
	}
	return schema, nil
}

func (s *Store) verify(ctx context.Context) error {
	schema, err := s.readSchema(ctx)
	if err != nil {
		return err
	}
	if schema != s.schema {
		return fmt.Errorf("%w: native index shape changed", vectorstore.ErrIncompatibleSpace)
	}
	return nil
}
func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := vectorstore.ValidateDocuments(docs); err != nil {
		return err
	}
	for _, d := range docs {
		if _, err := encodeRecord(d); err != nil {
			return err
		}
	}
	return s.verify(ctx)
}
func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := batch.ValidateFor(s.space); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	writes := make([]mongo.WriteModel, len(batch.Records))
	ids := make([]string, len(writes))
	for i, r := range batch.Records {
		doc := r.Document
		record, err := encodeRecord(doc)
		if err != nil {
			return err
		}
		v, err := s.schema.vector(r.Values)
		if err != nil {
			return err
		}
		record[s.schema.vectorPath] = v
		raw, err := bson.Marshal(record)
		if err != nil {
			return err
		}
		ids[i] = doc.ID
		writes[i] = mongo.NewReplaceOneModel().SetFilter(bson.M{idField: doc.ID, s.schema.vectorPath: bson.M{"$exists": true}}).SetReplacement(bson.Raw(raw)).SetUpsert(true).SetCollation(&options.Collation{Locale: "simple"})
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	result, err := s.collection.BulkWrite(ctx, writes, options.BulkWrite().SetOrdered(true))
	if err == nil {
		err = checkBulkAcknowledgment(result, len(writes))
	}
	if err == nil {
		return nil
	}
	return writeEvidence(ids, result, err)
}
func writeEvidence(ids []string, result *mongo.BulkWriteResult, cause error) *vectorstore.WriteError {
	var w vectorstore.WriteOutcome
	// Ordered writes stop at the first write error. A write concern or transport
	// failure on its own proves neither rejection nor lack of delivery.
	var failure mongo.BulkWriteException
	stop := len(ids)
	if errors.As(cause, &failure) && len(failure.WriteErrors) == 1 {
		i := failure.WriteErrors[0].Index
		if i >= 0 && i < len(ids) {
			stop = i
			w.Rejected = []string{ids[i]}
			w.Unattempted = ids[i+1:]
		}
	}
	if result == nil || failure.WriteConcernError != nil || !result.Acknowledged && stop == len(ids) {
		w.Unconfirmed = ids[:stop]
		return vectorstore.NewWriteError(cause, w)
	}
	for i, id := range ids[:stop] {
		if got, ok := result.UpsertedIDs[int64(i)].(string); ok && got == id {
			w.Acknowledged = append(w.Acknowledged, id)
		} else {
			w.Unconfirmed = append(w.Unconfirmed, id)
		}
	}
	// The driver finalizes aggregate counts on successful command completion;
	// ordered error returns can expose pre-normalization counts.
	if result.Acknowledged && stop == len(ids) {
		total := result.MatchedCount + result.UpsertedCount
		if result.MatchedCount >= 0 && result.UpsertedCount >= int64(len(w.Acknowledged)) && total >= int64(len(w.Acknowledged)) && total <= int64(len(ids)) {
			w.AcknowledgedCount = new(int(total) - len(w.Acknowledged))
		}
	}
	return vectorstore.NewWriteError(cause, w)
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

func (s *Store) nativeTerm(p filter.Predicate) (string, error) {
	if !s.schema.indexedTerms {
		return "", vectorstore.ErrUnsupportedFilter
	}
	return keyword.EqualString(p)
}
func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, opts vectorstore.SearchOptions) error {
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := opts.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return err
	}
	if _, err := s.searchCandidates(opts.ResultLimit()); err != nil {
		return err
	}
	if opts.Filter != nil {
		if _, err := s.nativeTerm(opts.Filter); err != nil {
			return err
		}
	}
	return s.verify(ctx)
}
func (s *Store) Query(ctx context.Context, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	return s.rank(ctx, q, nil)
}
func (s *Store) QueryByScan(ctx context.Context, q *vectorstore.Query, budget vectorstore.ScanBudget) (*vectorstore.SearchResponse, error) {
	if err := q.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if q.Options.Filter == nil {
		return nil, vectorstore.ErrMissingFilter
	}
	if err := budget.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget.Timeout)
	defer cancel()
	return s.rank(ctx, q, &budget)
}
func (s *Store) rank(ctx context.Context, request *vectorstore.Query, budget *vectorstore.ScanBudget) (response *vectorstore.SearchResponse, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			err = request.ValidateResponse(response)
		}
		if err != nil {
			response = nil
		}
	}()
	candidates, err := s.searchCandidates(request.Options.ResultLimit())
	if err != nil {
		return nil, err
	}
	query, err := s.schema.vector(request.Values)
	if err != nil {
		return nil, err
	}
	var nativeWhere bson.M
	if budget == nil && request.Options.Filter != nil {
		term, termErr := s.nativeTerm(request.Options.Filter)
		if termErr != nil {
			return nil, termErr
		}
		nativeWhere = bson.M{termsField: bson.M{"$in": []string{term}}}
	}
	if verifyErr := s.verify(ctx); verifyErr != nil {
		return nil, verifyErr
	}
	var selected []selectedDocument
	if budget != nil {
		selected, err = s.selectDocuments(ctx, request.Options.Filter, *budget)
		if err != nil {
			return nil, err
		}
		if len(selected) == 0 {
			return &vectorstore.SearchResponse{}, nil
		}
	}
	var ids []string
	for _, record := range selected {
		ids = append(ids, record.id)
	}
	groups := [][]string{nil}
	if budget != nil {
		groups = slices.Collect(slices.Chunk(ids, filterPageSize))
	}
	var matches []*vectorstore.SearchResult
	seen := make(map[string]struct{})
	for _, group := range groups {
		native := bson.M{"index": s.vectorIndexName, "path": s.schema.vectorPath, "queryVector": query, "numCandidates": candidates, "limit": request.Options.ResultLimit()}
		if nativeWhere != nil {
			native["filter"] = nativeWhere
		}
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

func (s *Store) selectDocuments(ctx context.Context, predicate filter.Predicate, budget vectorstore.ScanBudget) (selected []selectedDocument, err error) {
	cursor, err := s.collection.Aggregate(ctx, scanPipeline(s.schema.vectorPath, budget), options.Aggregate().SetCollation(&options.Collation{Locale: "simple"}))
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
		within, ok := cursor.Current.Lookup("within").BooleanOK()
		if !ok {
			return nil, errors.New("mongodb: malformed scan budget evidence")
		}
		if !within {
			return nil, vectorstore.ErrScanBudget
		}
		source, ok := cursor.Current.Lookup("document").DocumentOK()
		if !ok {
			return nil, errors.New("mongodb: missing scan document")
		}
		doc, facts, decodeErr := s.schema.decode(source, false)
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
	term, err := s.nativeTerm(predicate)
	if err != nil {
		return err
	}
	if verifyErr := s.verify(ctx); verifyErr != nil {
		return verifyErr
	}
	result, err := s.collection.DeleteMany(ctx, bson.M{s.schema.vectorPath: bson.M{"$exists": true}, termsField: term}, options.DeleteMany().SetHint(bson.D{{Key: termsField, Value: 1}}).SetCollation(&options.Collation{Locale: "simple"}))
	if err != nil {
		return err
	}
	if result == nil || !result.Acknowledged || result.DeletedCount < 0 {
		return errors.New("mongodb: unconfirmed predicate deletion")
	}
	return nil
}
func (s *Store) DeleteMatching(ctx context.Context, predicate filter.Predicate, budget vectorstore.ScanBudget) error {
	if err := budget.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, budget.Timeout)
	defer cancel()
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	records, err := s.selectDocuments(ctx, predicate, budget)
	if err != nil {
		return err
	}
	for _, record := range records {
		constraint := bson.M{s.schema.vectorPath: bson.M{"$exists": true}, idField: record.id, "$expr": bson.M{"$eq": bson.A{"$" + metadataField, bson.M{"$literal": record.metadataJSON}}}}
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
	if err := s.verify(ctx); err != nil {
		return err
	}
	result, err := s.collection.DeleteMany(ctx, bson.M{idField: bson.M{"$in": selected}, s.schema.vectorPath: bson.M{"$exists": true}}, options.DeleteMany().SetCollation(&options.Collation{Locale: "simple"}))
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
		return 0, fmt.Errorf("mongodb.Store.Query: TopK %d exceeds the %d candidates $vectorSearch can consider", limit, MaxNumCandidates)
	}
	return max(s.numCandidates, limit), nil
}
