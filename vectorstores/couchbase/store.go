package couchbase

import (
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/couchbase/gocb/v2"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const Provider = "Couchbase"

const (
	DefaultScopeName = "_default"

	DefaultSimilarity         = SimilarityDotProduct
	rankingBatchSize          = 512
	maximumCollectionKeyBytes = 246
)

// StoreConfig wires a host-owned SDK cluster and an exclusively managed document
// collection. The host owns connecting and closing the cluster.
type StoreConfig struct {
	Cluster    *gocb.Cluster
	BucketName string
	ScopeName  string
	Space      vectorstore.SpaceID
	Dimensions int
	Similarity Similarity
	// InitializeSchema creates the collection's default primary query index;
	// the bucket, scope and collection must already exist.
	InitializeSchema bool
}

// CollectionName returns the actual collection the host must provision in its
// chosen bucket and scope. Changing coordinate space or width selects another
// physical collection and cannot mix vectors in an existing one.
func (s StoreConfig) CollectionName() string {
	return s.Space.StorageKey() + "_" + strconv.Itoa(s.Dimensions)
}
func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Cluster == nil {
		return errors.New("couchbase: Cluster is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
	}
	if s.Dimensions <= 0 {
		return errors.New("couchbase: positive Dimensions required")
	}
	if !s.Similarity.Valid() {
		return fmt.Errorf("couchbase: unsupported Similarity %q", s.Similarity)
	}
	for _, field := range []struct{ name, value string }{{"BucketName", s.BucketName}, {"ScopeName", s.ScopeName}, {"CollectionName", s.CollectionName()}} {
		if err := identifier(field.value).validate(field.name); err != nil {
			return err
		}
	}
	return nil
}

func (s *StoreConfig) applyDefaults() {
	s.ScopeName = cmp.Or(s.ScopeName, DefaultScopeName)

	s.Similarity = cmp.Or(s.Similarity, DefaultSimilarity)
}

var (
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type Store struct {
	scope      *gocb.Scope
	collection *gocb.Collection
	keyspace   string
	space      vectorstore.SpaceID
	vectors    vectorPolicy
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scope := config.Cluster.Bucket(config.BucketName).Scope(config.ScopeName)
	store := &Store{
		scope: scope, collection: scope.Collection(config.CollectionName()),
		keyspace: fmt.Sprintf("`%s`.`%s`.`%s`", config.BucketName, config.ScopeName, config.CollectionName()),
		space:    config.Space, vectors: vectorPolicy{dimensions: config.Dimensions, similarity: config.Similarity},
	}
	if config.InitializeSchema {
		if err := store.collection.QueryIndexes().CreatePrimaryIndex(&gocb.CreatePrimaryQueryIndexOptions{Context: ctx, IgnoreIfExists: true}); err != nil {
			return nil, fmt.Errorf("couchbase: create primary index: %w", errors.Join(err, ctx.Err()))
		}
	}
	// Probe the actual query capability without model I/O.
	probeRows := 0
	if err := store.runStatement(ctx, fmt.Sprintf(`SELECT VECTOR_DISTANCE(c.embedding, [1,0], %q) AS distance FROM [{"embedding":[1,0]}] AS c`, config.Similarity), nil, func(raw json.RawMessage) error {
		probeRows++
		var row distanceRow
		if err := jsonv2.Unmarshal(raw, &row, jsonv2.RejectUnknownMembers(true)); err != nil {
			return err
		}
		if row.Distance == nil {
			return errors.New("couchbase: native vector distance is missing")
		}
		return config.Similarity.score(*row.Distance).Validate()
	}); err != nil {
		return nil, fmt.Errorf("couchbase: require native vector distance: %w", err)
	}
	if probeRows != 1 {
		return nil, errors.New("couchbase: native vector distance probe is incomplete")
	}
	if err := store.runStatement(ctx, "SELECT RAW META(c).id FROM "+store.keyspace+" AS c LIMIT 1", nil, nil); err != nil {
		return nil, fmt.Errorf("couchbase: require queryable collection: %w", err)
	}
	return store, nil
}

// Upsert preflights the entire raw vector batch before individually acknowledged KV writes.
func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := batch.ValidateFor(s.space); err != nil {
		return err
	}
	payloads := make([][]byte, 0, len(batch.Records))
	ids := make([]string, 0, len(batch.Records))
	for _, r := range batch.Records {
		d := r.Document
		if d.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
		if err := validateKey(d.ID); err != nil {
			return err
		}
		payload, err := encodeStoredDocument(d, r.Values, s.vectors)
		if err != nil {
			return err
		}
		payloads = append(payloads, payload)
		ids = append(ids, d.ID)
	}
	for i, id := range ids {
		if err := ctx.Err(); err != nil {
			return vectorstore.NewWriteError(err, vectorstore.WriteOutcome{Acknowledged: ids[:i], Unattempted: ids[i:]})
		}
		result, err := s.collection.Upsert(id, payloads[i], &gocb.UpsertOptions{Context: ctx, Transcoder: gocb.NewRawJSONTranscoder()})
		if err != nil || result == nil || result.Cas() == 0 {
			if err == nil {
				err = errors.New("couchbase: missing mutation acknowledgment")
			}
			return vectorstore.NewWriteError(errors.Join(err, ctx.Err()), vectorstore.WriteOutcome{Acknowledged: ids[:i], Unconfirmed: ids[i : i+1], Unattempted: ids[i+1:]})
		}
	}
	return nil
}
func (s *Store) QueryByScan(ctx context.Context, request *vectorstore.Query, budget vectorstore.ScanBudget) (*vectorstore.SearchResponse, error) {
	if err := request.ValidateFor(s.space); err != nil {
		return nil, fmt.Errorf("couchbase.Store.QueryByScan: %w", err)
	}
	if err := request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("couchbase.Store.QueryByScan: %w", err)
	}
	if err := s.vectors.validate(request.Values); err != nil {
		return nil, err
	}
	meter, err := budget.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer meter.Close()
	ctx = meter.Context()
	candidates, err := s.selectDocuments(ctx, request.Options.Filter, meter, budget.MaxRecords+1)
	if err != nil {
		return nil, err
	}
	var ranked []rankedResult
	if len(candidates) != 0 {
		vector := request.Values
		for batch := range slices.Chunk(candidates, rankingBatchSize) {
			projections := make([]vectorProjection, len(batch))
			for i, candidate := range batch {
				projections[i] = vectorProjection{Position: i, Embedding: candidate.embedding}
			}
			seen := make([]bool, len(batch))
			err := s.runStatement(ctx, fmt.Sprintf("SELECT c.position, VECTOR_DISTANCE(c.embedding, $vector, %q) AS distance FROM $candidates AS c", s.vectors.similarity), map[string]any{"candidates": projections, "vector": vector}, func(raw json.RawMessage) error {
				var row distanceRow
				if err := jsonv2.Unmarshal(raw, &row, jsonv2.RejectUnknownMembers(true)); err != nil {
					return fmt.Errorf("couchbase: decode distance: %w", err)
				}
				if row.Position == nil || *row.Position < 0 || *row.Position >= len(batch) || seen[*row.Position] || row.Distance == nil {
					return errors.New("couchbase: invalid native distance result")
				}
				seen[*row.Position] = true
				hit, err := vectorstore.NewSearchResult(batch[*row.Position].document, s.vectors.similarity.score(*row.Distance))
				if err != nil {
					return err
				}
				if hit.Score >= request.Options.MinScore {
					ranked = append(ranked, rankedResult{result: hit, distance: *row.Distance})
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			if slices.Contains(seen, false) {
				return nil, errors.New("couchbase: native distance result is incomplete")
			}
			slices.SortFunc(ranked, compareResults)
			if len(ranked) > request.Options.ResultLimit() {
				ranked = ranked[:request.Options.ResultLimit()]
			}
		}
	}
	results := make([]*vectorstore.SearchResult, len(ranked))
	for i, hit := range ranked {
		results[i] = hit.result
	}
	response := &vectorstore.SearchResponse{Results: results}
	if err := request.ValidateResponse(response); err != nil {
		return nil, err
	}
	return response, nil
}

// selectDocuments captures each complete KV record once. Filtering, ranking and
// returned content use that same projection even if another writer replaces it.
func (s *Store) selectDocuments(ctx context.Context, predicate filter.Predicate, meter *vectorstore.ScanMeter, readLimit int) ([]storedCandidate, error) {
	var candidates []storedCandidate
	err := s.runStatement(ctx, "SELECT META(c).id AS id, META(c).cas AS cas, c AS record FROM "+s.keyspace+" AS c LIMIT $readLimit", map[string]any{"readLimit": readLimit}, func(raw json.RawMessage) error {
		if err := meter.Charge(int64(len(raw))); err != nil {
			return err
		}
		candidate, err := decodeStoredRow(raw, s.vectors)
		if err != nil {
			return err
		}
		if predicate != nil {
			values, err := candidate.document.Metadata.Values()
			if err != nil {
				return err
			}
			match, err := filter.Match(predicate, values)
			if err != nil {
				return fmt.Errorf("couchbase: evaluate filter for %s: %w", candidate.document.ID, err)
			}
			if !match {
				return nil
			}
		}
		candidates = append(candidates, candidate)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

func (s *Store) runStatement(ctx context.Context, stmt string, params map[string]any, row func(json.RawMessage) error) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	result, err := s.scope.Query(stmt, &gocb.QueryOptions{Context: ctx, NamedParameters: params, ScanConsistency: gocb.QueryScanConsistencyRequestPlus, UseReplica: gocb.QueryUseReplicaLevelOff, Readonly: true})
	if err != nil {
		return fmt.Errorf("couchbase: query: %w", errors.Join(err, ctx.Err()))
	}
	defer func() { err = errors.Join(err, result.Close()) }()
	for result.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if row != nil {
			var raw json.RawMessage
			if err := result.Row(&raw); err != nil {
				return fmt.Errorf("couchbase: read row: %w", err)
			}
			if err := row(raw); err != nil {
				return err
			}
		}
	}
	if err := result.Err(); err != nil {
		return fmt.Errorf("couchbase: read result: %w", errors.Join(err, ctx.Err()))
	}
	return ctx.Err()
}

// DeleteMatching preflights the entire collection before the first mutation. CAS
// prevents deletion of a replacement that was never evaluated by Core.
func (s *Store) DeleteMatching(ctx context.Context, predicate filter.Predicate, budget vectorstore.ScanBudget) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return fmt.Errorf("couchbase.Store.DeleteMatching: %w", err)
	}
	meter, err := budget.Begin(ctx)
	if err != nil {
		return err
	}
	defer meter.Close()
	ctx = meter.Context()
	candidates, err := s.selectDocuments(ctx, predicate, meter, budget.MaxRecords+1)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if _, err := s.collection.Remove(candidate.document.ID, &gocb.RemoveOptions{Context: ctx, Cas: candidate.cas}); err != nil {
			if errors.Is(err, gocb.ErrDocumentNotFound) {
				continue
			}
			return fmt.Errorf("couchbase: remove evaluated version of %s: %w", candidate.document.ID, errors.Join(err, ctx.Err()))
		}
	}
	return nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := validateKey(id); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if _, err := s.collection.Remove(id, &gocb.RemoveOptions{Context: ctx}); err != nil {
			if errors.Is(err, gocb.ErrDocumentNotFound) {
				continue
			}
			return fmt.Errorf("couchbase: remove %s: %w", id, errors.Join(err, ctx.Err()))
		}
	}
	return nil
}

func compareResults(left, right rankedResult) int {
	if order := cmp.Compare(left.distance, right.distance); order != 0 {
		return order
	}
	return cmp.Compare(left.result.Document.ID, right.result.Document.ID)
}

func validateKey(id string) error {
	if id == "" || len(id) > maximumCollectionKeyBytes || !utf8.ValidString(id) {
		return fmt.Errorf("couchbase: %w: KV key must contain 1..%d bytes", vectorstore.ErrInvalidDocument, maximumCollectionKeyBytes)
	}
	return nil
}
