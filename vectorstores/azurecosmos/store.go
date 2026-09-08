package azurecosmos

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

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
	DefaultContentField      = "content"
	DefaultMetadataField     = "metadata"
	DefaultEmbeddingField    = "embedding"
	DefaultPartitionKeyField = "partition_key"
	docAlias                 = "c"
)

// ErrIncompatibleContainer reports a container that cannot serve this store:
// it is partitioned on another path, declares no vector embedding at the
// configured field, or declares one with a different distance function.
var ErrIncompatibleContainer = errors.New("azurecosmos: container is incompatible")

// DistanceFunction names the function passed to VectorDistance(). The value is
// the container's own, read from its vector embedding policy at construction,
// because VectorDistance answers with a raw number and nothing that says which
// function produced it.
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

	// PartitionKey is the non-empty string value of the logical partition
	// owned by this store. Index, Search, and DeleteWhere use this same value.
	// The Go SDK cannot execute ranked vector queries across partitions.
	PartitionKey string

	// PartitionKeyField is the top-level JSON field containing PartitionKey.
	// The container must be provisioned with /<PartitionKeyField> as its
	// partition-key path. Zero uses [DefaultPartitionKeyField].
	PartitionKeyField string

	// ContentField / MetadataField / EmbeddingField override the JSON
	// property names on stored documents. Cosmos requires the ID field "id".
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
	if s.PartitionKey == "" {
		return errors.New("azurecosmos: PartitionKey is required")
	}
	if !s.DistanceFunction.Valid() {
		return fmt.Errorf("azurecosmos: unsupported DistanceFunction %q", s.DistanceFunction)
	}
	return s.validateIdentifiers()
}

func (s StoreConfig) validateIdentifiers() error {
	fields := []struct{ name, value string }{
		{"ContentField", s.ContentField},
		{"MetadataField", s.MetadataField},
		{"EmbeddingField", s.EmbeddingField},
		{"PartitionKeyField", s.PartitionKeyField},
	}
	seen := map[string]string{"id": "document ID"}
	for _, field := range fields {
		if err := identifier(field.value).validate(field.name); err != nil {
			return err
		}
		if previous, duplicate := seen[field.value]; duplicate {
			return fmt.Errorf("azurecosmos: %s conflicts with %s at JSON field %q", field.name, previous, field.value)
		}
		seen[field.value] = field.name
	}
	return nil
}

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	s.ContentField = cmp.Or(s.ContentField, DefaultContentField)
	s.MetadataField = cmp.Or(s.MetadataField, DefaultMetadataField)
	s.EmbeddingField = cmp.Or(s.EmbeddingField, DefaultEmbeddingField)
	s.PartitionKeyField = cmp.Or(s.PartitionKeyField, DefaultPartitionKeyField)
	s.DistanceFunction = cmp.Or(s.DistanceFunction, DistanceCosine)
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
)

// Store implements vector-store capabilities within one Azure Cosmos DB
// logical partition.
// The container must have a vector policy matching
// [StoreConfig.DistanceFunction] and the embedding model's dimensions.
type Store struct {
	container         *azcosmos.ContainerClient
	contentField      string
	metadataField     string
	embeddingField    string
	partitionKey      string
	partitionKeyField string
	embeddingClient   embeddingclient.Client
	documentBatcher   vectorstore.Batcher
	distanceFunction  DistanceFunction
}

// NewStore confirms the container agrees with the configured distance function
// and partition-key path during construction, which is why it takes a context:
// a store returned with the wrong function would go on returning scores that
// are wrong rather than absent, and the misconfiguration is at wiring.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("azurecosmos: create embedding client: %w", err)
	}

	container, err := config.Container.Read(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("azurecosmos: read container %s: %w", config.Container.ID(), err)
	}
	if err = validateContainer(container.ContainerProperties,
		config.EmbeddingField, config.PartitionKeyField, config.DistanceFunction); err != nil {
		return nil, err
	}

	return &Store{
		container:         config.Container,
		contentField:      config.ContentField,
		metadataField:     config.MetadataField,
		embeddingField:    config.EmbeddingField,
		partitionKey:      config.PartitionKey,
		partitionKeyField: config.PartitionKeyField,
		embeddingClient:   embeddingClient,
		documentBatcher:   config.DocumentBatcher,
		distanceFunction:  config.DistanceFunction,
	}, nil
}

// validateContainer refuses a container that cannot serve this store.
//
// The distance function is the silent half: VectorDistance() returns a raw
// number and nothing that says which function produced it, so a wrong value
// makes the store apply the wrong transformation and return plausible scores
// that are wrong, with MinScore filtering by a threshold in the wrong scale.
//
// The partition-key path is checked from the same read because it is the one
// other thing the store assumes about a container it did not create: every
// item is written with the key under PartitionKeyField, and a container
// partitioned on some other path would file all of them under an absent key.
//
// Dimensions are deliberately not compared. This store declares none, and
// Cosmos rejects a vector of the wrong width on write.
func validateContainer(
	properties *azcosmos.ContainerProperties,
	embeddingField string,
	partitionKeyField string,
	want DistanceFunction,
) error {
	if properties == nil {
		return fmt.Errorf("%w: the container returned no properties", ErrIncompatibleContainer)
	}

	wantPath := "/" + partitionKeyField
	if paths := properties.PartitionKeyDefinition.Paths; !slices.Contains(paths, wantPath) {
		return fmt.Errorf("%w: container %s is partitioned on %v, but the store writes its key at %s",
			ErrIncompatibleContainer, properties.ID, paths, wantPath)
	}

	if properties.VectorEmbeddingPolicy == nil {
		return fmt.Errorf("%w: container %s declares no vector embedding policy, so VectorDistance has nothing to search",
			ErrIncompatibleContainer, properties.ID)
	}
	wantVectorPath := "/" + embeddingField
	for _, embedded := range properties.VectorEmbeddingPolicy.VectorEmbeddings {
		if embedded.Path != wantVectorPath {
			continue
		}
		if DistanceFunction(embedded.DistanceFunction) != want {
			return fmt.Errorf("%w: container %s declares %s with distance function %q, but the store is configured for %q",
				ErrIncompatibleContainer, properties.ID, wantVectorPath, embedded.DistanceFunction, want)
		}
		return nil
	}
	return fmt.Errorf("%w: container %s declares no vector embedding at %s",
		ErrIncompatibleContainer, properties.ID, wantVectorPath)
}

// Index embeds documents and upserts them into the store's bound partition.
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
				"id":                id,
				s.partitionKeyField: s.partitionKey,
				s.contentField:      doc.Text,
				s.metadataField:     lo.CoalesceMapOrEmpty(metadataValues),
				s.embeddingField:    embedding.Float32Vector(vectors[i]),
			}
			body, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("azurecosmos: marshal item %s: %w", id, err)
			}
			if _, err := s.container.UpsertItem(ctx, azcosmos.NewPartitionKeyString(s.partitionKey), body, nil); err != nil {
				return fmt.Errorf("azurecosmos: upsert %s: %w", id, err)
			}
		}
	}
	return nil
}

// Search runs a VectorDistance-ordered query within the store's bound partition.
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
		"SELECT TOP @topK c.id AS _id, c.%s AS _content, c.%s AS _metadata, %s AS _vector_score FROM c%s ORDER BY %s",
		s.contentField, s.metadataField, distanceCall, whereClause, distanceCall,
	)

	queryParams := []azcosmos.QueryParameter{
		{Name: "@queryVec", Value: queryVec},
		{Name: "@topK", Value: req.Options.ResultLimit()},
	}
	for _, p := range params {
		queryParams = append(queryParams, azcosmos.QueryParameter{Name: p.Name, Value: p.Value})
	}

	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKeyString(s.partitionKey), &azcosmos.QueryOptions{
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

// DeleteWhere removes matching documents in the bound partition. It completes
// enumeration before deletion so mutation cannot invalidate query continuation.
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

	query := fmt.Sprintf("SELECT c.id AS _id FROM c WHERE %s", predicate)
	queryParams := make([]azcosmos.QueryParameter, 0, len(params))
	for _, p := range params {
		queryParams = append(queryParams, azcosmos.QueryParameter{Name: p.Name, Value: p.Value})
	}

	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKeyString(s.partitionKey), &azcosmos.QueryOptions{
		QueryParameters: queryParams,
	})

	// Complete enumeration before deleting so writes cannot invalidate the
	// continuation used to select the remaining matches.
	var ids []string
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
			ids = append(ids, holder.ID)
		}
	}
	for _, id := range ids {
		if _, err := s.container.DeleteItem(ctx, azcosmos.NewPartitionKeyString(s.partitionKey), id, nil); err != nil {
			return fmt.Errorf("azurecosmos: delete %s: %w", id, err)
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
	// Metadata stays raw so a stored integer beyond the exact float64 range
	// reaches the caller unchanged.
	var row struct {
		ID          string       `json:"_id"`
		Content     string       `json:"_content"`
		Metadata    metadata.Map `json:"_metadata"`
		VectorScore *float64     `json:"_vector_score"`
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
	return &vectorstore.SearchResult{
		Document: &document.Document{ID: row.ID, Text: row.Content, Metadata: row.Metadata},
		Score:    score,
	}, nil
}
