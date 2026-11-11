package azurecosmos

import (
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"unicode/utf8"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/document"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const Provider = "AzureCosmosDB"

var ErrIncompatibleContainer = errors.New("azurecosmos: container is incompatible")

type StoreConfig struct {
	Container    *azcosmos.ContainerClient
	PartitionKey string
	Space        vectorstore.SpaceID
}

// physicalPartition is an actual Cosmos routing key; the prefix is fixed-width,
// so logical keys cannot escape their vector space by containing a delimiter.
func (s StoreConfig) physicalPartition() string { return s.Space.StorageKey() + ":" + s.PartitionKey }

func (s StoreConfig) Validate() error {
	if s.Container == nil {
		return errors.New("azurecosmos: Container is required")
	}
	if s.PartitionKey == "" || !utf8.ValidString(s.PartitionKey) || len(s.PartitionKey) > partitionKeyV2MaxBytes {
		return fmt.Errorf("azurecosmos: PartitionKey must contain 1 to %d valid UTF-8 bytes", partitionKeyV2MaxBytes)
	}
	return s.Space.Validate()
}

var (
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.Querier   = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type Store struct {
	container    *azcosmos.ContainerClient
	partitionKey string
	space        vectorstore.SpaceID
	schema       containerSchema
}

func NewStore(ctx context.Context, cfg StoreConfig) (*Store, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Store{container: cfg.Container, space: cfg.Space, partitionKey: cfg.physicalPartition()}
	schema, err := s.binding(ctx)
	if err != nil {
		return nil, err
	}
	s.schema = schema
	return s, nil
}

func (s *Store) binding(ctx context.Context) (containerSchema, error) {
	response, err := s.container.Read(ctx, nil)
	if err != nil {
		return containerSchema{}, fmt.Errorf("azurecosmos: read container: %w", err)
	}
	return newContainerSchema(response.ContainerProperties, s.container.ID(), s.partitionKey)
}

func (s *Store) verify(ctx context.Context) error {
	schema, err := s.binding(ctx)
	if err != nil {
		return err
	}
	if schema != s.schema {
		return vectorstore.ErrIncompatibleSpace
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
		if _, err := encodeDocument(d, s.partitionKey); err != nil {
			return err
		}
	}
	return s.verify(ctx)
}

func (s *Store) Upsert(ctx context.Context, batch *vectorstore.Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := batch.ValidateFor(s.space); err != nil {
		return err
	}
	ids := make([]string, len(batch.Records))
	bodies := make([][]byte, len(ids))
	for i, r := range batch.Records {
		rec, err := encodeDocument(r.Document, s.partitionKey)
		if err != nil {
			return err
		}
		v, err := s.schema.narrow(r.Values)
		if err != nil {
			return err
		}
		rec.Embedding = new(corevector.Coordinates[float32](v))
		bodies[i], err = rec.marshal()
		if err != nil {
			return err
		}
		ids[i] = r.Document.ID
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	return s.writeItems(ctx, ids, func(i int) error {
		_, err := s.container.UpsertItem(ctx, azcosmos.NewPartitionKeyString(s.partitionKey), bodies[i], nil)
		return err
	})
}

// writeItems records exactly which single-item requests were issued. A server
// timeout or lost response cannot determine whether the current item changed.
func (s *Store) writeItems(ctx context.Context, ids []string, write func(int) error) error {
	for i, id := range ids {
		if err := ctx.Err(); err != nil {
			return vectorstore.NewWriteError(err, vectorstore.WriteOutcome{Acknowledged: ids[:i], Unattempted: ids[i:]})
		}
		if err := write(i); err != nil {
			outcome := vectorstore.WriteOutcome{Acknowledged: ids[:i], Unconfirmed: []string{id}, Unattempted: ids[i+1:]}
			var response *azcore.ResponseError
			if errors.As(err, &response) {
				switch response.StatusCode {
				case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed, http.StatusRequestEntityTooLarge, http.StatusTooManyRequests:
					outcome.Rejected, outcome.Unconfirmed = outcome.Unconfirmed, nil
				}
			}
			return vectorstore.NewWriteError(err, outcome)
		}
	}
	return nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	keys, err := vectorstore.DistinctIDs(ids)
	if err != nil {
		return err
	}
	for _, id := range keys {
		if err := validateID(id); err != nil {
			return err
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	return s.writeItems(ctx, keys, func(i int) error {
		_, err := s.container.DeleteItem(ctx, azcosmos.NewPartitionKeyString(s.partitionKey), keys[i], nil)
		var response *azcore.ResponseError
		if errors.As(err, &response) && response.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	})
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

type rankedResult struct {
	raw    float64
	result *vectorstore.SearchResult
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
	if err := opts.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return err
	}
	if opts.Filter != nil {
		return vectorstore.ErrUnsupportedFilter
	}
	return s.verify(ctx)
}

// The parameters of the ranked query; the statement refers to each by name.
const (
	queryVectorParam = "@queryVec"
	topKParam        = "@topK"
)

func (s *Store) Query(ctx context.Context, request *vectorstore.Query) (response *vectorstore.SearchResponse, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err = s.CheckQuery(ctx, request.Space, request.Options); err != nil {
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
	queryVector, err := s.schema.narrow(request.Values)
	if err != nil {
		return nil, err
	}
	var ranked []rankedResult
	seen := make(map[string]struct{})
	parameters := []azcosmos.QueryParameter{{Name: queryVectorParam, Value: queryVector}, {Name: topKParam, Value: request.Options.ResultLimit()}}
	distance := "VectorDistance(c.embedding, " + queryVectorParam + ")"
	sql := "SELECT TOP " + topKParam + " c AS item, " + distance + " AS score FROM c ORDER BY " + distance
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
		if len(seen) > request.Options.ResultLimit() {
			return errors.New("azurecosmos: service returned more records than requested")
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
	slices.SortFunc(ranked, func(left, right rankedResult) int {
		return cmp.Or(cmp.Compare(s.schema.nearness(right.raw), s.schema.nearness(left.raw)), cmp.Compare(left.result.Document.ID, right.result.Document.ID))
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

func (s *Store) selectItems(ctx context.Context, request vectorstore.ScanRequest) ([]selectedItem, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	meter, err := request.Budget.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer meter.Close()
	ctx = meter.Context()
	if err = s.verify(ctx); err != nil {
		return nil, err
	}
	var selected []selectedItem
	seen := make(map[string]bool)
	err = s.query(ctx, "SELECT VALUE c FROM c", nil, func(raw []byte) error {
		if chargeErr := meter.Charge(int64(len(raw))); chargeErr != nil {
			return chargeErr
		}
		doc, etag, decodeErr := s.schema.decodeDocument(raw, s.partitionKey)
		if decodeErr != nil {
			return decodeErr
		}
		if seen[doc.ID] {
			return errors.New("azurecosmos: partition scan repeated an item ID")
		}
		seen[doc.ID] = true
		if request.Filter != nil {
			matched, matchErr := filter.Match(request.Filter, doc.Metadata)
			if matchErr != nil || !matched {
				return matchErr
			}
		}
		selected = append(selected, selectedItem{doc: doc, etag: etag})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return selected, nil
}

// Scan evaluates Core metadata predicates after charging every raw Cosmos item.
// Cosmos continuation pages do not form a multi-page transactional snapshot.
func (s *Store) Scan(ctx context.Context, request vectorstore.ScanRequest) ([]*document.Document, error) {
	selected, err := s.selectItems(ctx, request)
	if err != nil {
		return nil, err
	}
	docs := make([]*document.Document, len(selected))
	for i, item := range selected {
		docs[i] = item.doc
	}
	return docs, nil
}

// DeleteMatching verifies the complete authorized scan before the first delete.
// A replacement is protected by its native ETag. Deletes are individually
// acknowledged, not atomic across a partition; failures return a WriteError.
func (s *Store) DeleteMatching(ctx context.Context, predicate filter.Predicate, budget vectorstore.ScanBudget) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	selected, err := s.selectItems(ctx, vectorstore.ScanRequest{Budget: budget, Filter: predicate})
	if err != nil {
		return err
	}
	ids := make([]string, len(selected))
	for i, r := range selected {
		ids[i] = r.doc.ID
	}
	return s.writeItems(ctx, ids, func(i int) error {
		_, err := s.container.DeleteItem(ctx, azcosmos.NewPartitionKeyString(s.partitionKey), ids[i], &azcosmos.ItemOptions{IfMatchEtag: &selected[i].etag})
		return err
	})
}
