package azurecosmos

import (
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const Provider = "AzureCosmosDB"

// Bound escaped 1023-byte IDs and a 4096-component vector below the native
// request budget; this is a transport grouping policy, not a result limit.
const maxQueryIDs = 100

var ErrIncompatibleContainer = errors.New("azurecosmos: container is incompatible")

type StoreConfig struct {
	Container       *azcosmos.ContainerClient
	PartitionKey    string
	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher
}

func (s StoreConfig) Validate() error {
	if s.Container == nil {
		return errors.New("azurecosmos: Container is required")
	}
	if s.PartitionKey == "" || !utf8.ValidString(s.PartitionKey) || len(s.PartitionKey) > 2048 {
		return errors.New("azurecosmos: PartitionKey must contain 1 to 2048 valid UTF-8 bytes")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("azurecosmos: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("azurecosmos: DocumentBatcher is required")
	}
	return nil
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
)

type Store struct {
	container       *azcosmos.ContainerClient
	partitionKey    string
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	schema          containerSchema
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	client, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	response, err := config.Container.Read(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("azurecosmos: read container: %w", err)
	}
	schema, err := newContainerSchema(response.ContainerProperties, config.Container.ID(), config.PartitionKey)
	if err != nil {
		return nil, err
	}
	store := &Store{container: config.Container, partitionKey: config.PartitionKey, embeddingClient: client, documentBatcher: config.DocumentBatcher, schema: schema}
	if _, err = store.selectItems(ctx, nil); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	records := make(map[string]itemRecord, len(request.Documents))
	for _, doc := range request.Documents {
		record, err := encodeDocument(doc, s.partitionKey)
		if err != nil {
			return err
		}
		records[doc.ID] = record
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	var bodies [][]byte
	for _, batch := range batches {
		texts, textErr := batch.Texts()
		if textErr != nil {
			return textErr
		}
		vectors, embedErr := s.embeddingClient.EmbedTexts(ctx, texts)
		if embedErr != nil {
			return embedErr
		}
		for i, doc := range batch.Documents {
			vector, vectorErr := s.schema.narrow(vectors[i])
			if vectorErr != nil {
				return fmt.Errorf("azurecosmos: document %s: %w", doc.ID, vectorErr)
			}
			record := records[doc.ID]
			record.Embedding = &vector
			body, bodyErr := record.marshal()
			if bodyErr != nil {
				return bodyErr
			}
			bodies = append(bodies, body)
		}
	}
	for _, body := range bodies {
		if _, writeErr := s.container.UpsertItem(ctx, azcosmos.NewPartitionKeyString(s.partitionKey), body, nil); writeErr != nil {
			return fmt.Errorf("azurecosmos: upsert item: %w", writeErr)
		}
	}
	return nil
}

func (s *Store) query(ctx context.Context, query string, parameters []azcosmos.QueryParameter, visit func([]byte) error) error {
	pager := s.container.NewQueryItemsPager(query, azcosmos.NewPartitionKeyString(s.partitionKey), &azcosmos.QueryOptions{QueryParameters: parameters})
	tokens := make(map[string]struct{})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("azurecosmos: query items: %w", err)
		}
		for _, raw := range page.Items {
			if err = visit(raw); err != nil {
				return err
			}
		}
		if page.ContinuationToken != nil && *page.ContinuationToken != "" {
			if _, repeated := tokens[*page.ContinuationToken]; repeated {
				return errors.New("azurecosmos: query repeated a continuation token")
			}
			tokens[*page.ContinuationToken] = struct{}{}
		}
	}
	return nil
}

type selectedItem struct {
	id   string
	etag azcore.ETag
}

func (s *Store) selectItems(ctx context.Context, predicate filter.Predicate) ([]selectedItem, error) {
	var selected []selectedItem
	seen := make(map[string]struct{})
	err := s.query(ctx, "SELECT VALUE c FROM c", nil, func(raw []byte) error {
		doc, etag, err := s.schema.decodeDocument(raw, s.partitionKey)
		if err != nil {
			return err
		}
		if _, repeated := seen[doc.ID]; repeated {
			return errors.New("azurecosmos: partition scan repeated an item ID")
		}
		seen[doc.ID] = struct{}{}
		if predicate != nil {
			values, valueErr := doc.Metadata.Values()
			if valueErr != nil {
				return valueErr
			}
			matched, matchErr := filter.Match(predicate, values)
			if matchErr != nil {
				return matchErr
			}
			if !matched {
				return nil
			}
		}
		selected = append(selected, selectedItem{id: doc.ID, etag: etag})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return selected, nil
}

type rankedResult struct {
	raw    float64
	result *vectorstore.SearchResult
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
	selected, err := s.selectItems(ctx, request.Options.Filter)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{}}, nil
	}
	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, err
	}
	queryVector, err := s.schema.narrow(vector)
	if err != nil {
		return nil, err
	}
	groups := [][]selectedItem{nil}
	if request.Options.Filter != nil {
		groups = slices.Collect(slices.Chunk(selected, maxQueryIDs))
	}
	var ranked []rankedResult
	seen := make(map[string]struct{})
	for _, group := range groups {
		parameters := []azcosmos.QueryParameter{{Name: "@queryVec", Value: queryVector}, {Name: "@topK", Value: request.Options.ResultLimit()}}
		where := ""
		allowed := make(map[string]struct{}, len(group))
		if group != nil {
			names := make([]string, len(group))
			for i, item := range group {
				name := fmt.Sprintf("@id%d", i)
				names[i] = name
				parameters = append(parameters, azcosmos.QueryParameter{Name: name, Value: item.id})
				allowed[item.id] = struct{}{}
			}
			where = " WHERE c.id IN (" + strings.Join(names, ",") + ")"
			parameters[1].Value = min(len(group), request.Options.ResultLimit())
		}
		distance := "VectorDistance(c.embedding, @queryVec)"
		sql := "SELECT TOP @topK c AS item, " + distance + " AS score FROM c" + where + " ORDER BY " + distance
		queryErr := s.query(ctx, sql, parameters, func(raw []byte) error {
			var row struct {
				Item  json.RawMessage `json:"item"`
				Score *float64        `json:"score"`
			}
			if decodeErr := jsonv2.Unmarshal(raw, &row, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
				return decodeErr
			}
			if row.Score == nil {
				return errors.New("azurecosmos: native result is missing its numeric score")
			}
			doc, _, decodeErr := s.schema.decodeDocument(row.Item, s.partitionKey)
			if decodeErr != nil {
				return decodeErr
			}
			if _, duplicate := seen[doc.ID]; duplicate {
				return errors.New("azurecosmos: ranked queries repeated an item ID")
			}
			seen[doc.ID] = struct{}{}
			if group != nil {
				if _, member := allowed[doc.ID]; !member {
					return errors.New("azurecosmos: native query returned an ID outside Core membership")
				}
				values, valueErr := doc.Metadata.Values()
				if valueErr != nil {
					return valueErr
				}
				matched, matchErr := filter.Match(request.Options.Filter, values)
				if matchErr != nil {
					return matchErr
				}
				if !matched {
					return errors.New("azurecosmos: native result changed Core membership during search")
				}
			}
			score, scoreErr := s.schema.score(*row.Score)
			if scoreErr != nil {
				return scoreErr
			}
			result, resultErr := vectorstore.NewSearchResult(doc, score)
			if resultErr != nil {
				return resultErr
			}
			ranked = append(ranked, rankedResult{raw: *row.Score, result: result})
			return nil
		})
		if queryErr != nil {
			return nil, queryErr
		}
	}
	slices.SortFunc(ranked, func(left, right rankedResult) int {
		order := cmp.Compare(left.raw, right.raw)
		if s.schema.function != azcosmos.VectorDistanceFunctionEuclidean {
			order = -order
		}
		if order != 0 {
			return order
		}
		return cmp.Compare(left.result.Document.ID, right.result.Document.ID)
	})
	results := make([]*vectorstore.SearchResult, 0, min(len(ranked), request.Options.ResultLimit()))
	for _, row := range ranked {
		if row.result.Score < request.Options.MinScore {
			continue
		}
		results = append(results, row.result)
		if len(results) == request.Options.ResultLimit() {
			break
		}
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	selected, err := s.selectItems(ctx, predicate)
	if err != nil {
		return err
	}
	for _, item := range selected {
		etag := item.etag
		if _, deleteErr := s.container.DeleteItem(ctx, azcosmos.NewPartitionKeyString(s.partitionKey), item.id, &azcosmos.ItemOptions{IfMatchEtag: &etag}); deleteErr != nil {
			return fmt.Errorf("azurecosmos: delete observed item %s: %w", item.id, deleteErr)
		}
	}
	return nil
}
