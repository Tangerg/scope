package inmemory

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// StoreConfig requires an embedding model. A nil Similarity selects CosineSimilarity.
type StoreConfig struct {
	EmbeddingModel embedding.Model

	Similarity Similarity
}

func (s *StoreConfig) applyDefaults() {
	if s.Similarity == nil {
		s.Similarity = CosineSimilarity
	}
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.EmbeddingModel) {
		return ErrMissingEmbeddingModel
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
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store is a concurrency-safe, in-process vector store. Index snapshots documents
// and Search returns detached results in deterministic score order. Search scans
// the full index; all records are lost when the process exits.
type Store struct {
	embeddingClient embeddingclient.Client
	similarity      Similarity

	mu         sync.RWMutex
	records    map[string]record
	dimensions int
}

func NewStore(_ context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("inmemory: create store: create embedding client: %w", err)
	}
	return &Store{
		embeddingClient: embeddingClient,
		similarity:      config.Similarity,
		records:         map[string]record{},
	}, nil
}

func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("inmemory: index documents: %w", err)
	}
	docs := make([]*document.Document, len(request.Documents))
	texts := make([]string, len(docs))
	for i, doc := range request.Documents {
		docs[i] = doc.Clone()
		texts[i] = docs[i].Text
	}

	embeddings, err := s.embeddingClient.EmbedTexts(ctx, texts)
	if err != nil {
		return fmt.Errorf("inmemory: index documents: embed: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("inmemory: index documents: %w", err)
	}
	dimensions := len(embeddings[0])
	if s.dimensions != 0 && s.dimensions != dimensions {
		return fmt.Errorf("inmemory: index documents: embedding dimensions %d do not match index dimensions %d", dimensions, s.dimensions)
	}
	s.dimensions = dimensions
	for i, doc := range docs {
		s.records[doc.ID] = record{doc: doc, embedding: embeddings[i]}
	}
	return nil
}

func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (*vectorstore.SearchResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("inmemory: search: %w", err)
	}
	if err := req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("inmemory: search: %w", err)
	}

	query, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("inmemory: search: embed query: %w", err)
	}

	candidates, err := s.searchCandidates(ctx, query, req.Options)
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
	if err := response.ValidateFor(req); err != nil {
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
	if s.dimensions != 0 && len(query) != s.dimensions {
		return nil, fmt.Errorf("inmemory: search: query dimensions %d do not match index dimensions %d", len(query), s.dimensions)
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
	for _, id := range ids {
		delete(s.records, id)
	}
	return nil
}

// Clear removes all records and resets the index embedding dimensions.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.records)
	s.dimensions = 0
}
