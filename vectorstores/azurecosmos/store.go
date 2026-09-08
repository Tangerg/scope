package azurecosmos

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Provider is the stable backend name for host-side attribution.
const Provider = "AzureCosmosDB"

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultIDField        = "id"
	DefaultContentField   = "content"
	DefaultMetadataField  = "metadata"
	DefaultEmbeddingField = "embedding"
	DefaultPartitionKey   = "/id"
	docAlias              = "c"
)

// DistanceFunction names the function passed to VectorDistance().
// The chosen value must match the container's vector embedding
// policy.
type DistanceFunction string

// The metric is a closed vocabulary because score direction and threshold
// semantics depend on it: the same raw number means "near" under one metric and
// "far" under another, so an unrecognized value must be rejected rather than
// guessed.
const (
	DistanceCosine     DistanceFunction = "cosine"
	DistanceDotProduct DistanceFunction = "dotproduct"
	DistanceEuclidean  DistanceFunction = "euclidean"
)

func (d DistanceFunction) Valid() bool {
	switch d {
	case DistanceCosine, DistanceDotProduct, DistanceEuclidean:
		return true
	default:
		return false
	}
}

func (d DistanceFunction) String() string { return string(d) }

func (d DistanceFunction) score(raw float64) vectorstore.Score {
	switch d {
	case DistanceEuclidean:
		return vectorstore.ScoreFromDistance(raw)
	case DistanceDotProduct:
		return vectorstore.ScoreFromInnerProduct(raw)
	case DistanceCosine:
		fallthrough
	default:
		return vectorstore.ScoreFromCosineSimilarity(raw)
	}
}

// StoreConfig contains configuration options for the Azure Cosmos DB
// NoSQL vector store.
type StoreConfig struct {
	// Container is the Cosmos container that holds the documents.
	// The caller is responsible for provisioning it with the right
	// vector embedding policy + indexing policy (set up in Azure
	// Portal / ARM / Terraform). Required.
	Container *azcosmos.ContainerClient

	// PartitionKeyPath is the container's partition-key path,
	// recorded so the store can compute partition keys for upsert
	// and delete. Optional: defaults to [DefaultPartitionKey] ("/id").
	PartitionKeyPath string

	// IDField / ContentField / MetadataField / EmbeddingField
	// override the JSON property names on the stored documents.
	IDField        string
	ContentField   string
	MetadataField  string
	EmbeddingField string

	// EmbeddingModel produces vectors for the documents. Required.
	EmbeddingModel embedding.Model

	// DocumentBatcher batches documents before upsert. Required.
	DocumentBatcher vectorstore.Batcher

	// DistanceFunction selects the function passed to
	// VectorDistance(). Optional: defaults to [DistanceCosine].
	DistanceFunction DistanceFunction
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Container == nil {
		return errors.New("azurecosmos: Container is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("azurecosmos: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("azurecosmos: DocumentBatcher is required")
	}
	if !s.DistanceFunction.Valid() {
		return fmt.Errorf("azurecosmos: unsupported DistanceFunction %q", s.DistanceFunction)
	}
	return s.validateIdentifiers()
}

func (s StoreConfig) validateIdentifiers() error {
	if err := identifier(s.IDField).validate("IDField"); err != nil {
		return err
	}
	if err := identifier(s.ContentField).validate("ContentField"); err != nil {
		return err
	}
	if err := identifier(s.MetadataField).validate("MetadataField"); err != nil {
		return err
	}
	return identifier(s.EmbeddingField).validate("EmbeddingField")
}

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	s.IDField = cmp.Or(s.IDField, DefaultIDField)
	s.ContentField = cmp.Or(s.ContentField, DefaultContentField)
	s.MetadataField = cmp.Or(s.MetadataField, DefaultMetadataField)
	s.EmbeddingField = cmp.Or(s.EmbeddingField, DefaultEmbeddingField)
	s.PartitionKeyPath = cmp.Or(s.PartitionKeyPath, DefaultPartitionKey)
	s.DistanceFunction = cmp.Or(s.DistanceFunction, DistanceCosine)
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
)

// Store implements vector-store capabilities with Azure Cosmos DB for NoSQL.
// The container must have a vector policy matching
// [StoreConfig.DistanceFunction] and the embedding model's dimensions.
type Store struct {
	container        *azcosmos.ContainerClient
	idField          string
	contentField     string
	metadataField    string
	embeddingField   string
	partitionKeyPath string
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	distanceFunction DistanceFunction
}

// NewStore needs no context because construction performs no I/O; the
// container is provisioned outside this package, so the store only validates
// configuration and assembles state.
func NewStore(config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("azurecosmos: create embedding client: %w", err)
	}

	return &Store{
		container:        config.Container,
		idField:          config.IDField,
		contentField:     config.ContentField,
		metadataField:    config.MetadataField,
		embeddingField:   config.EmbeddingField,
		partitionKeyPath: config.PartitionKeyPath,
		embeddingClient:  embeddingClient,
		documentBatcher:  config.DocumentBatcher,
		distanceFunction: config.DistanceFunction,
	}, nil
}

// Index embeds documents and upserts them.
func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("azurecosmos.Store.Index: %w", validateErr)
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("azurecosmos: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("azurecosmos: embed documents: %w", err)
		}

		for i, doc := range docs {
			id := doc.ID
			metadataValues, err := doc.Metadata.Values()
			if err != nil {
				return fmt.Errorf("azurecosmos: decode metadata for %s: %w", id, err)
			}
			payload := map[string]any{
				s.idField:        id,
				s.contentField:   doc.Text,
				s.metadataField:  lo.CoalesceMapOrEmpty(metadataValues),
				s.embeddingField: embedding.Float32Vector(vectors[i]),
			}
			body, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("azurecosmos: marshal item %s: %w", id, err)
			}
			if _, err := s.container.UpsertItem(ctx, azcosmos.NewPartitionKeyString(id), body, nil); err != nil {
				return fmt.Errorf("azurecosmos: upsert %s: %w", id, err)
			}
		}
	}
	return nil
}

// Search runs a VectorDistance-ordered query.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("azurecosmos.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("azurecosmos.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("azurecosmos: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)

	wherePredicate, params, err := s.buildFilter(req.Options.Filter)
	if err != nil {
		return nil, err
	}

	whereClause := ""
	if wherePredicate != "" {
		whereClause = " WHERE " + wherePredicate
	}

	distanceCall := fmt.Sprintf("VectorDistance(c.%s, @queryVec, false, {'distanceFunction':'%s'})",
		s.embeddingField, s.distanceFunction)

	query := fmt.Sprintf(
		"SELECT TOP @topK c.%s AS _id, c.%s AS _content, c.%s AS _metadata, %s AS _vector_score FROM c%s ORDER BY %s",
		s.idField, s.contentField, s.metadataField, distanceCall, whereClause, distanceCall,
	)

	queryParams := []azcosmos.QueryParameter{
		{Name: "@queryVec", Value: queryVec},
		{Name: "@topK", Value: req.Options.ResultLimit()},
	}
	for _, p := range params {
		queryParams = append(queryParams, azcosmos.QueryParameter{Name: p.Name, Value: p.Value})
	}

	// Cross-partition query: pass the canonical empty partition key.
	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKey(), &azcosmos.QueryOptions{
		QueryParameters: queryParams,
	})

	docs = make([]*vectorstore.SearchResult, 0, req.Options.ResultLimit())
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("azurecosmos: query: %w", err)
		}
		for _, item := range page.Items {
			match, err := s.decodeRow(item, req.Options.MinScore)
			if err != nil {
				return nil, err
			}
			if match != nil {
				docs = append(docs, match)
			}
		}
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("azurecosmos.Store.DeleteWhere: %w", err)
	}

	predicate, params, err := s.buildFilter(expr)
	if err != nil {
		return err
	}
	if predicate == "" {
		return errors.New("azurecosmos: refusing to delete on empty filter")
	}

	query := fmt.Sprintf("SELECT c.%s AS _id FROM c WHERE %s", s.idField, predicate)
	queryParams := make([]azcosmos.QueryParameter, 0, len(params))
	for _, p := range params {
		queryParams = append(queryParams, azcosmos.QueryParameter{Name: p.Name, Value: p.Value})
	}

	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKey(), &azcosmos.QueryOptions{
		QueryParameters: queryParams,
	})

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("azurecosmos: enumerate ids: %w", err)
		}
		for _, item := range page.Items {
			var holder struct {
				ID string `json:"_id"`
			}
			if err := json.Unmarshal(item, &holder); err != nil {
				return fmt.Errorf("azurecosmos: decode id: %w", err)
			}
			if _, err := s.container.DeleteItem(ctx, azcosmos.NewPartitionKeyString(holder.ID), holder.ID, nil); err != nil {
				return fmt.Errorf("azurecosmos: delete %s: %w", holder.ID, err)
			}
		}
	}
	return nil
}

func (s *Store) buildFilter(expr filter.Predicate) (string, []NamedParam, error) {
	if expr == nil {
		return "", nil, nil
	}
	v := newVisitor(docAlias, s.metadataField)
	if err := expr.Accept(v); err != nil {
		return "", nil, fmt.Errorf("azurecosmos: convert filter: %w", err)
	}
	predicate, params := v.snapshot()
	return predicate, params, nil
}

// decodeRow turns a Cosmos JSON row into a Document and applies Scope's
// normalized score threshold.
func (s *Store) decodeRow(raw json.RawMessage, minScore vectorstore.Score) (*vectorstore.SearchResult, error) {
	var row struct {
		ID          string         `json:"_id"`
		Content     string         `json:"_content"`
		Metadata    map[string]any `json:"_metadata"`
		VectorScore *float64       `json:"_vector_score"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, fmt.Errorf("azurecosmos: decode row: %w", err)
	}

	if row.VectorScore == nil {
		return nil, errors.New("azurecosmos: result is missing numeric _vector_score")
	}
	score := s.distanceFunction.score(*row.VectorScore)
	if score < minScore {
		return nil, nil
	}
	if row.ID == "" {
		return nil, errors.New("azurecosmos: result is missing _id")
	}
	if row.Content == "" {
		return nil, errors.New("azurecosmos: result is missing _content")
	}
	metadata, err := metadata.FromValues(row.Metadata)
	if err != nil {
		return nil, fmt.Errorf("azurecosmos: convert metadata: %w", err)
	}
	return &vectorstore.SearchResult{
		Document: &document.Document{ID: row.ID, Text: row.Content, Metadata: metadata},
		Score:    score,
	}, nil
}

func (s *Store) Close() error { return nil }
