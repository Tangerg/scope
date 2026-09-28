package pinecone

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/pinecone-io/go-pinecone/v4/pinecone"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Documented Pinecone operation limits. A request past either of them is
// rejected by the service, so the store either splits the work or refuses
// locally instead of sending one that cannot succeed.
const (
	// MaxTopK is the largest number of results one query may return.
	MaxTopK = 10_000

	// MaxVectorsPerUpsert is the largest number of records one upsert may
	// carry. Pinecone also caps the request at 2 MB, which the store cannot
	// predict from the record count alone; that limit surfaces as an error.
	MaxVectorsPerUpsert = 1_000
)

// Provider is the stable backend name for host-side attribution.
const (
	Provider = "Pinecone"
)

const (
	// payloadDocumentContentKey is the metadata key for saving document content.
	payloadDocumentContentKey = "scope:ai:vectorstore:pinecone:payload_document_content"
)

// DistanceMetric records the similarity metric configured on the existing
// Pinecone index. The data-plane connection does not expose index metadata, so
// the value is declared here and checked against the control plane at
// construction.
type DistanceMetric string

// The metric is a closed vocabulary because score direction and threshold
// semantics depend on it: the same raw number means "near" under one metric and
// "far" under another, so an unrecognized value must be rejected rather than
// guessed.
const (
	DistanceCosine    DistanceMetric = "cosine"
	DistanceDot       DistanceMetric = "dotproduct"
	DistanceEuclidean DistanceMetric = "euclidean"
)

func (d DistanceMetric) Valid() bool {
	switch d {
	case DistanceCosine, DistanceDot, DistanceEuclidean:
		return true
	default:
		return false
	}
}

func (d DistanceMetric) String() string { return string(d) }

func (d DistanceMetric) score(raw float64) vectorstore.Score {
	switch d {
	case DistanceCosine:
		return vectorstore.ScoreFromCosineSimilarity(raw)
	case DistanceDot:
		return vectorstore.ScoreFromInnerProduct(raw)
	case DistanceEuclidean:
		return vectorstore.ScoreFromDistance(raw)
	default:
		return vectorstore.ScoreFromValue(raw)
	}
}

// StoreConfig contains configuration options for Pinecone vector store.
type StoreConfig struct {
	// Client is the Pinecone client instance.
	// Required: must be provided, otherwise initialization will fail.
	Client *pinecone.Client

	// IndexHost is the host URL of the Pinecone index.
	// Required: must be a non-empty string.
	// Obtain it from DescribeIndex or the Pinecone web console.
	IndexHost string

	// Namespace is the index namespace to use for all operations.
	// Optional: defaults to the default namespace if empty.
	Namespace string

	// EmbeddingModel is the model used to generate vector embeddings from text.
	// Required: must be provided.
	EmbeddingModel embedding.Model

	// DocumentBatcher is responsible for batching documents before insertion.
	// Required: must be provided.
	DocumentBatcher vectorstore.Batcher

	// DistanceMetric is the metric the index was created with. Required
	// because Pinecone returns metric-specific raw scores. NewStore reads the
	// index's own metric and refuses a mismatch with [ErrIncompatibleIndex],
	// so this states a fact rather than carrying an unchecked obligation.
	DistanceMetric DistanceMetric
}

func (s StoreConfig) Validate() error {
	if s.Client == nil {
		return ErrMissingClient
	}
	if s.IndexHost == "" {
		return ErrMissingIndexHost
	}
	if lo.IsNil(s.EmbeddingModel) {
		return ErrMissingEmbeddingModel
	}
	if lo.IsNil(s.DocumentBatcher) {
		return ErrMissingDocumentBatcher
	}
	if s.DistanceMetric == "" {
		return ErrMissingDistanceMetric
	}
	if !s.DistanceMetric.Valid() {
		return fmt.Errorf("pinecone: unsupported DistanceMetric %q", s.DistanceMetric)
	}
	return nil
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// indexConnection is the narrow Pinecone data-plane surface the store depends
// on. It stays unexported because the store opens the connection itself; it
// exists so the store's requirements are visible and its acknowledgment
// handling is checkable. *pinecone.IndexConnection satisfies it.
type indexConnection interface {
	UpsertVectors(context.Context, []*pinecone.Vector) (uint32, error)
	QueryByVectorValues(context.Context, *pinecone.QueryByVectorValuesRequest) (*pinecone.QueryVectorsResponse, error)
	ListVectors(context.Context, *pinecone.ListVectorsRequest) (*pinecone.ListVectorsResponse, error)
	FetchVectors(context.Context, []string) (*pinecone.FetchVectorsResponse, error)
	DeleteVectorsById(context.Context, []string) error
	Close() error
}

// Store implements [vectorstore.Store] against a Pinecone index. Pinecone owns
// index creation and dimensionality, so this type validates against the index
// it is pointed at rather than provisioning one.
type Store struct {
	index           indexConnection
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	distanceMetric  DistanceMetric
}

// NewStore confirms the index agrees with the configured metric during
// construction, which is why it takes a context: a store returned with the
// wrong metric would go on returning scores that are wrong rather than absent,
// and the misconfiguration is at wiring.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("pinecone: create embedding client: %w", err)
	}

	if err = verifyIndexMetric(ctx, config); err != nil {
		return nil, err
	}

	idx, err := config.Client.Index(pinecone.NewIndexConnParams{
		Host:      config.IndexHost,
		Namespace: config.Namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("pinecone: connect to index at %s: %w", config.IndexHost, err)
	}

	return &Store{
		index:           idx,
		embeddingClient: embeddingClient,
		documentBatcher: config.DocumentBatcher,
		distanceMetric:  config.DistanceMetric,
	}, nil
}

// verifyIndexMetric reads the index's own metric and refuses a configured value
// that disagrees.
//
// The metric lives on the control plane; the data plane never reports it. A key
// with custom permissions may be denied there, and Pinecone documents that case
// as ordinary — it is why a caller "must target your index by host when
// performing data operations", which is exactly the shape of
// [StoreConfig.IndexHost]. A store built for a host-targeted key cannot then
// demand control-plane read as the price of construction, so a denial leaves
// the configured metric unverified rather than failing. Every other failure is
// reported: a store that cannot tell why it could not look has not established
// anything.
func verifyIndexMetric(ctx context.Context, config StoreConfig) error {
	indexes, err := config.Client.ListIndexes(ctx)
	if err != nil {
		if isAuthorizationDenied(err) {
			return nil
		}
		return fmt.Errorf("pinecone: list indexes: %w", err)
	}
	return validateIndexMetric(indexes, config.IndexHost, config.DistanceMetric)
}

// isAuthorizationDenied reports whether Pinecone refused the call for lack of
// permission rather than for any other reason.
func isAuthorizationDenied(err error) bool {
	var pineconeErr *pinecone.PineconeError
	if !errors.As(err, &pineconeErr) {
		return false
	}
	return pineconeErr.Code == http.StatusUnauthorized || pineconeErr.Code == http.StatusForbidden
}

// validateIndexMetric refuses a store whose configured metric is not the one
// the index was created with.
//
// The metric decides what a raw Pinecone score means, so getting it wrong does
// not fail: cosine reads an unbounded inner product as a similarity, euclidean
// reads a similarity as a distance, and MinScore then filters by the wrong
// direction. The result is plausible ranked output that is wrong, which is the
// one failure a caller cannot detect downstream.
//
// Dimensionality is deliberately not compared. This store declares no
// dimension of its own, and a vector of the wrong width is rejected by
// Pinecone on the first upsert or query — that half already fails loudly.
func validateIndexMetric(indexes []*pinecone.Index, host string, want DistanceMetric) error {
	wantHost, err := normalizeIndexHost(host)
	if err != nil {
		return err
	}
	for _, index := range indexes {
		indexHost, hostErr := normalizeIndexHost(index.Host)
		if hostErr != nil {
			continue
		}
		if indexHost != wantHost {
			continue
		}
		if DistanceMetric(index.Metric) != want {
			return fmt.Errorf("%w: index %s at %s was created with metric %q, but the store is configured for %q",
				ErrIncompatibleIndex, index.Name, host, index.Metric, want)
		}
		return nil
	}
	return fmt.Errorf("%w: no index in this project is served at %s", ErrIncompatibleIndex, host)
}

// normalizeIndexHost reduces a host to the form Pinecone reports it in.
// StoreConfig.IndexHost accepts a bare host or a URL, because the SDK's own
// connection helper adds the scheme when one is missing, while ListIndexes
// answers with the bare host.
func normalizeIndexHost(host string) (string, error) {
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	parsed, err := url.Parse(host)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("pinecone: IndexHost %q is not a valid host or URL", host)
	}
	return parsed.Host, nil
}

func (s *Store) buildVectors(docs []*document.Document, vectors [][]float64) ([]*pinecone.Vector, error) {
	result := make([]*pinecone.Vector, len(docs))

	for i, doc := range docs {
		values := embedding.Float32Vector(vectors[i])

		point := &pinecone.Vector{
			Id:     doc.ID,
			Values: &values,
		}

		metadataValues, err := doc.Metadata.Values()
		if err != nil {
			return nil, fmt.Errorf("pinecone: decode metadata for document %s: %w", doc.ID, err)
		}
		metaMap := make(map[string]any, len(metadataValues)+1)
		maps.Copy(metaMap, metadataValues)
		metaMap[payloadDocumentContentKey] = doc.Text

		meta, err := payloadValues(metaMap)
		if err != nil {
			return nil, fmt.Errorf("pinecone: convert metadata for document %s: %w", doc.ID, err)
		}
		point.Metadata = meta

		result[i] = point
	}

	return result, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("pinecone.Store.Index: %w", validateErr)
	}
	for index, doc := range request.Documents {
		if _, reserved := doc.Metadata[payloadDocumentContentKey]; reserved {
			return fmt.Errorf("pinecone: %w: document %d uses the reserved content metadata key", vectorstore.ErrInvalidDocument, index)
		}
		values, valuesErr := doc.Metadata.Values()
		if valuesErr != nil {
			return fmt.Errorf("pinecone: decode document %d metadata: %w", index, valuesErr)
		}
		if _, payloadErr := payloadValues(values); payloadErr != nil {
			return fmt.Errorf("pinecone: document %d metadata: %w", index, payloadErr)
		}
		if doc.Media != nil {
			return fmt.Errorf("pinecone.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("pinecone: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("pinecone: embed documents: %w", err)
		}

		points, err := s.buildVectors(docs, vectors)
		if err != nil {
			return err
		}

		// Pinecone caps one upsert at MaxVectorsPerUpsert, so the caller's
		// batch is split rather than sent as a request certain to be rejected.
		for chunk := range slices.Chunk(points, MaxVectorsPerUpsert) {
			// UpsertedCount is Pinecone's acknowledgment of the batch.
			// Accepting a short count would report a partial write as a
			// complete one.
			upserted, err := s.index.UpsertVectors(ctx, chunk)
			if err != nil {
				return fmt.Errorf("pinecone: upsert %d vectors: %w", len(chunk), err)
			}
			if uint64(upserted) != uint64(len(chunk)) {
				return fmt.Errorf("pinecone: upsert acknowledged %d of %d vectors", upserted, len(chunk))
			}
		}
	}

	return nil
}

func (s *Store) buildDocumentsFromScoredVectors(svs []*pinecone.ScoredVector, minScore vectorstore.Score) ([]*vectorstore.SearchResult, error) {
	docs := make([]*vectorstore.SearchResult, 0, len(svs))

	for i, sv := range svs {
		if sv == nil || sv.Vector == nil {
			return nil, fmt.Errorf("pinecone: query result %d is missing its vector record", i)
		}
		score := s.distanceMetric.score(float64(sv.Score))
		if score < minScore {
			continue
		}

		doc, err := s.toDocument(sv.Vector)
		if err != nil {
			return nil, err
		}

		docs = append(docs, &vectorstore.SearchResult{Document: doc, Score: score})
	}

	return docs, nil
}

func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("pinecone.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("pinecone.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("pinecone: embed query: %w", err)
	}

	if limit := req.Options.ResultLimit(); limit > MaxTopK {
		return nil, fmt.Errorf("pinecone.Store.Search: TopK %d exceeds the %d results a query can return",
			limit, MaxTopK)
	}
	queryReq := &pinecone.QueryByVectorValuesRequest{
		Vector:          embedding.Float32Vector(vector),
		TopK:            uint32(req.Options.ResultLimit()),
		IncludeMetadata: true,
	}

	if req.Options.Filter != nil {
		return s.searchMatchingVectors(ctx, req, queryReq.Vector)
	}

	resp, err := s.index.QueryByVectorValues(ctx, queryReq)
	if err != nil {
		return nil, fmt.Errorf("pinecone: query index: %w", err)
	}

	if resp == nil {
		return nil, errors.New("pinecone: query returned no response")
	}
	if len(resp.Matches) == 0 {
		return &vectorstore.SearchResponse{}, nil
	}

	docs, err = s.buildDocumentsFromScoredVectors(resp.Matches, req.Options.MinScore)
	if err != nil {
		return nil, fmt.Errorf("pinecone: build documents from results: %w", err)
	}

	return &vectorstore.SearchResponse{Results: docs}, nil
}

// DeleteWhere lists the full namespace and applies Core's exact predicate to
// fetched metadata before deleting matching IDs. The List API requires a
// serverless index. Reads and deletions are not an atomic snapshot; coordinate
// writers when a predicate must remain true until deletion.
func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) error {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := expr.Validate(); err != nil {
		return fmt.Errorf("pinecone.Store.DeleteWhere: %w", err)
	}
	var ids []string
	err := s.visitMatches(ctx, expr, func(point *pinecone.Vector) error { ids = append(ids, point.Id); return nil })
	if err != nil {
		return err
	}
	return s.DeleteIDs(ctx, ids)
}

// DeleteIDs removes vectors by their string ids. An empty slice is a
// no-op; unknown ids are silently ignored (idempotent). Implements
// [vectorstore.IDDeleter].
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}

	for batch := range slices.Chunk(ids, maximumIDsPerDelete) {
		if err := s.index.DeleteVectorsById(ctx, batch); err != nil {
			return fmt.Errorf("pinecone: delete vectors by ids: %w", err)
		}
	}

	return nil
}

func (s *Store) Close() error {
	return s.index.Close()
}

const (
	metadataListPageSize = uint32(100)
	maximumIDsPerDelete  = 1_000
)

// visitMatches reads every page before a caller can report a complete result.
// Pinecone's native equality also matches list elements and cannot preserve
// Core's scalar/collection distinction. No lossy native prefilter is applied.
func (s *Store) visitMatches(ctx context.Context, predicate filter.Predicate, visit func(*pinecone.Vector) error) error {
	var token *string
	seenTokens := make(map[string]struct{})
	seenIDs := make(map[string]struct{})
	for {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		page, err := s.index.ListVectors(ctx, &pinecone.ListVectorsRequest{Limit: new(metadataListPageSize), PaginationToken: token})
		if err != nil {
			return fmt.Errorf("pinecone: list metadata candidates (requires serverless List API): %w", err)
		}
		if page == nil {
			return errors.New("pinecone: list metadata candidates returned no response")
		}
		ids := make([]string, 0, len(page.VectorIds))
		for _, id := range page.VectorIds {
			if id == nil || *id == "" {
				return errors.New("pinecone: list returned an empty vector ID")
			}
			if _, duplicate := seenIDs[*id]; duplicate {
				return fmt.Errorf("pinecone: list repeated vector %q", *id)
			}
			seenIDs[*id] = struct{}{}
			ids = append(ids, *id)
		}
		if len(ids) > 0 {
			fetched, err := s.index.FetchVectors(ctx, ids)
			if err != nil {
				return fmt.Errorf("pinecone: fetch metadata candidates: %w", err)
			}
			if fetched == nil {
				return errors.New("pinecone: fetch metadata candidates returned no response")
			}
			for _, id := range ids {
				point, exists := fetched.Vectors[id]
				if !exists {
					return fmt.Errorf("pinecone: listed vector %q disappeared before fetch", id)
				}
				if point == nil || point.Id != id || point.Metadata == nil {
					return fmt.Errorf("pinecone: fetched vector %q omitted identity or metadata", id)
				}
				values := point.Metadata.AsMap()
				delete(values, payloadDocumentContentKey)
				matched, err := filter.Match(predicate, values)
				if err != nil {
					return fmt.Errorf("pinecone: evaluate metadata for %q: %w", id, err)
				}
				if matched {
					if err := visit(point); err != nil {
						return err
					}
				}
			}
		}
		if page.NextPaginationToken == nil || *page.NextPaginationToken == "" {
			return nil
		}
		next := *page.NextPaginationToken
		if _, duplicate := seenTokens[next]; duplicate {
			return errors.New("pinecone: list repeated a pagination token")
		}
		seenTokens[next] = struct{}{}
		token = new(next)
	}
}

type scoredDocument struct {
	result *vectorstore.SearchResult
	rank   float64
}

func (s *Store) searchMatchingVectors(ctx context.Context, request *vectorstore.SearchRequest, query []float32) (*vectorstore.SearchResponse, error) {
	var ranked []scoredDocument
	limit := request.Options.ResultLimit()
	err := s.visitMatches(ctx, request.Options.Filter, func(point *pinecone.Vector) error {
		if point.Values == nil {
			return fmt.Errorf("pinecone: fetched vector %q omitted values", point.Id)
		}
		raw, err := s.distanceMetric.vectorMetric(query, *point.Values)
		if err != nil {
			return fmt.Errorf("pinecone: score fetched vector %q: %w", point.Id, err)
		}
		score := s.distanceMetric.score(raw)
		if score < request.Options.MinScore {
			return nil
		}
		doc, err := s.toDocument(point)
		if err != nil {
			return err
		}
		rank := raw
		if s.distanceMetric == DistanceEuclidean {
			rank = -raw
		}
		result := scoredDocument{result: &vectorstore.SearchResult{Document: doc, Score: score}, rank: rank}
		position, _ := slices.BinarySearchFunc(ranked, result, func(left, right scoredDocument) int {
			if left.rank != right.rank {
				return cmp.Compare(right.rank, left.rank)
			}
			return cmp.Compare(left.result.Document.ID, right.result.Document.ID)
		})
		if position >= limit {
			return nil
		}
		ranked = slices.Insert(ranked, position, result)
		if len(ranked) > limit {
			ranked = ranked[:limit]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	results := make([]*vectorstore.SearchResult, len(ranked))
	for index, result := range ranked {
		results[index] = result.result
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

func (s *Store) toDocument(point *pinecone.Vector) (*document.Document, error) {
	if point == nil || point.Id == "" || point.Metadata == nil {
		return nil, errors.New("pinecone: vector record omitted identity or metadata")
	}
	values := point.Metadata.AsMap()
	content, ok := values[payloadDocumentContentKey].(string)
	if !ok || content == "" {
		return nil, fmt.Errorf("pinecone: vector %q omitted document text", point.Id)
	}
	delete(values, payloadDocumentContentKey)
	encoded, err := metadata.FromValues(values)
	if err != nil {
		return nil, fmt.Errorf("pinecone: decode metadata for %q: %w", point.Id, err)
	}
	return &document.Document{ID: point.Id, Text: content, Metadata: encoded}, nil
}

// vectorMetric applies the same metric as the index to fetched float32 values.
// Pinecone's euclidean score is squared L2, not its square root. Float64
// accumulators can represent every product of finite float32 coordinates.
func (d DistanceMetric) vectorMetric(left, right []float32) (float64, error) {
	if len(left) == 0 || len(left) != len(right) {
		return 0, errors.New("vector dimensions do not match")
	}
	var dot, leftMagnitude, rightMagnitude, squaredDistance float64
	for index, a := range left {
		b := float64(right[index])
		value := float64(a)
		if math.IsNaN(value) || math.IsNaN(b) || math.IsInf(value, 0) || math.IsInf(b, 0) {
			return 0, errors.New("vector contains non-finite coordinates")
		}
		dot += value * b
		leftMagnitude += value * value
		rightMagnitude += b * b
		delta := value - b
		squaredDistance += delta * delta
	}
	switch d {
	case DistanceCosine:
		if leftMagnitude == 0 || rightMagnitude == 0 {
			return 0, errors.New("cosine requires nonzero vectors")
		}
		return dot / (math.Sqrt(leftMagnitude) * math.Sqrt(rightMagnitude)), nil
	case DistanceDot:
		return dot, nil
	case DistanceEuclidean:
		return squaredDistance, nil
	default:
		return 0, fmt.Errorf("unsupported distance metric %q", d)
	}
}
