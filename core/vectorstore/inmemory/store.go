package inmemory

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// StoreConfig binds one immutable coordinate space and dimension. A nil Similarity selects CosineSimilarity.
type StoreConfig struct {
	Space      vectorstore.SpaceID
	Dimensions int

	Similarity Similarity
}

func (s *StoreConfig) applyDefaults() {
	if s.Similarity == nil {
		s.Similarity = CosineSimilarity
	}
}

func (s StoreConfig) Validate() error {
	if err := s.Space.Validate(); err != nil {
		return err
	}
	if s.Dimensions < 1 {
		return fmt.Errorf("%w: dimension must be positive", vectorstore.ErrInvalidRequest)
	}
	return nil
}

type record struct {
	doc       *document.Document
	embedding []float64
}

func (r record) matches(predicate filter.Predicate) (bool, error) {
	if predicate == nil {
		return true, nil
	}
	values, err := r.doc.Metadata.Values()
	if err != nil {
		return false, fmt.Errorf("metadata: %w", err)
	}
	matched, err := filter.Match(predicate, values)
	if err != nil {
		return false, fmt.Errorf("filter: %w", err)
	}
	return matched, nil
}

var (
	_ vectorstore.Upserter      = (*Store)(nil)
	_ vectorstore.Querier       = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store is a concurrency-safe, in-process vector store. Upsert snapshots documents and coordinates
// and Query returns detached results in deterministic score order. Query scans
// the full index; all records are lost when the process exits.
type Store struct {
	space      vectorstore.SpaceID
	similarity Similarity

	mu         sync.RWMutex
	records    map[string]record
	dimensions int
}

func NewStore(_ context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Store{space: config.Space, dimensions: config.Dimensions,
		similarity: config.Similarity,
		records:    map[string]record{},
	}, nil
}

func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// CheckDocuments rejects an invalid space or document set before an index
// writer spends an embedding call. The memory store supports complete media.
func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := space.Require(s.space); err != nil {
		return err
	}
	return vectorstore.ValidateDocuments(docs)
}

func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := batch.ValidateFor(s.space); err != nil {
		return err
	}
	if len(batch.Records[0].Values) != s.dimensions {
		return fmt.Errorf("%w: vector dimension %d; want %d", vectorstore.ErrIncompatibleSpace, len(batch.Records[0].Values), s.dimensions)
	}
	owned := make([]record, len(batch.Records))
	for i, r := range batch.Records {
		if err := ctx.Err(); err != nil {
			return err
		}
		owned[i] = record{doc: r.Document.Clone(), embedding: slices.Clone(r.Values)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("inmemory: index documents: %w", err)
	}
	for _, r := range owned {
		s.records[r.doc.ID] = r
	}
	return nil
}

func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, opts vectorstore.SearchOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := opts.Validate(); err != nil {
		return err
	}
	return opts.RequireMode(vectorstore.SearchModeSemantic)
}

func (s *Store) Query(ctx context.Context, req *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	if err := req.ValidateFor(s.space); err != nil {
		return nil, fmt.Errorf("inmemory: search: %w", err)
	}
	if err := s.CheckQuery(ctx, req.Space, req.Options); err != nil {
		return nil, fmt.Errorf("inmemory: search: %w", err)
	}

	candidates, err := s.searchCandidates(ctx, req.Values, req.Options)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(candidates, func(a, b scoredDocument) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.doc.ID, b.doc.ID))
	})

	limit := min(req.Options.ResultLimit(), len(candidates))
	out := make([]*vectorstore.SearchResult, 0, limit)
	for i := range limit {
		out = append(out, &vectorstore.SearchResult{Document: candidates[i].doc.Clone(), Score: candidates[i].score})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	response := &vectorstore.SearchResponse{Results: out}
	if err := req.ValidateResponse(response); err != nil {
		return nil, err
	}
	return response, nil
}

type scoredDocument struct {
	doc   *document.Document
	score vectorstore.Score
}

func (s *Store) searchCandidates(ctx context.Context, query []float64, options vectorstore.SearchOptions) ([]scoredDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(query) != s.dimensions {
		return nil, fmt.Errorf("%w: query dimensions %d do not match index dimensions %d", vectorstore.ErrIncompatibleSpace, len(query), s.dimensions)
	}
	candidates := make([]scoredDocument, 0, len(s.records))
	for _, rec := range s.records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		matched, err := rec.matches(options.Filter)
		if err != nil {
			return nil, fmt.Errorf("inmemory: search: %w", err)
		}
		if !matched {
			continue
		}
		score := s.similarity(query, rec.embedding)
		if err := score.Validate(); err != nil {
			return nil, fmt.Errorf("inmemory: search: similarity score: %w", err)
		}
		if score < options.MinScore {
			continue
		}
		candidates = append(candidates, scoredDocument{doc: rec.doc, score: score})
	}
	return candidates, ctx.Err()
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) error {
	if lo.IsNil(expr) {
		return vectorstore.ErrMissingFilter
	}
	if err := expr.Validate(); err != nil {
		return fmt.Errorf("inmemory: delete by filter: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for id, rec := range s.records {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("inmemory: delete by filter: %w", err)
		}
		match, err := rec.matches(expr)
		if err != nil {
			return fmt.Errorf("inmemory: delete by filter: %w", err)
		}
		if match {
			delete(s.records, id)
		}
	}
	return nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("inmemory: delete by ID: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("inmemory: delete by ID: %w", err)
	}
	for _, id := range ids {
		delete(s.records, id)
	}
	return nil
}

// Clear removes all records while retaining the bound coordinate space.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.records)
}
