package weaviate

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/go-openapi/strfmt"
	"github.com/samber/lo"
	weaviateclient "github.com/weaviate/weaviate-go-client/v5/weaviate"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/fault"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/filters"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/graphql"
	"github.com/weaviate/weaviate/entities/models"

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
	client          *weaviateclient.Client
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	className       string
	schema          nativeSchema
	hybridAlpha     *float32
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	model, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	class, err := config.Client.Schema().ClassGetter().WithClassName(config.ClassName).Do(ctx)
	if err != nil {
		return nil, err
	}
	var schema nativeSchema
	if err = schema.read(class, config.ClassName); err != nil {
		return nil, err
	}
	var alpha *float32
	if config.HybridAlpha != nil {
		alpha = new(*config.HybridAlpha)
	}
	store := &Store{client: config.Client, embeddingClient: model, documentBatcher: config.DocumentBatcher, className: config.ClassName, schema: schema, hybridAlpha: alpha}
	if _, _, err = store.selectDocuments(ctx, nil); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	for _, doc := range request.Documents {
		if doc.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
	}
	records := make(map[string]*models.Object, len(request.Documents))
	for _, doc := range request.Documents {
		object, err := encodeRecord(doc, s.className)
		if err != nil {
			return err
		}
		records[doc.ID] = object
	}
	_, dimensions, err := s.selectDocuments(ctx, nil)
	if err != nil {
		return err
	}
	batches, err := request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return err
	}
	var objects []*models.Object
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
			vector, vectorErr := nativeVector(vectors[i], dimensions)
			if vectorErr != nil {
				return vectorErr
			}
			if dimensions == 0 {
				dimensions = len(vector)
			}
			object := records[doc.ID]
			object.Vector = models.C11yVector(vector)
			objects = append(objects, object)
		}
	}
	responses, err := s.client.Batch().ObjectsBatcher().WithObjects(objects...).Do(ctx)
	if err != nil {
		return err
	}
	return checkBatchAcknowledgments(objects, responses)
}

func checkBatchAcknowledgments(objects []*models.Object, responses []models.ObjectsGetResponse) error {
	if len(responses) != len(objects) {
		return fmt.Errorf("weaviate: batch insert returned %d results for %d objects",
			len(responses), len(objects))
	}
	remaining := make(map[strfmt.UUID]struct{}, len(objects))
	for _, object := range objects {
		remaining[object.ID] = struct{}{}
	}
	for index := range responses {
		response := &responses[index]
		result := response.Result
		if result == nil {
			return fmt.Errorf("weaviate: batch insert has no result for object %s", response.ID)
		}
		if result.Errors != nil {
			return fmt.Errorf("weaviate: batch insert error for object %s: %v",
				response.ID, result.Errors.Error)
		}
		if result.Status == nil || *result.Status != models.ObjectsGetResponseAO2ResultStatusSUCCESS {
			return fmt.Errorf("weaviate: batch insert for object %s reported status %s",
				response.ID, lo.FromPtrOr(result.Status, "none"))
		}
		if _, requested := remaining[response.ID]; !requested {
			return fmt.Errorf("weaviate: batch insert acknowledged an unrequested or repeated object %q", response.ID)
		}
		delete(remaining, response.ID)
	}
	return nil
}

func (s *Store) selectDocuments(ctx context.Context, predicate filter.Predicate) ([]selectedDocument, int, error) {
	var selected []selectedDocument
	dimensions := 0
	after := ""
	for {
		if err := context.Cause(ctx); err != nil {
			return nil, 0, err
		}
		builder := s.client.Data().ObjectsGetter().WithClassName(s.className).WithVector().WithLimit(metadataScanPageSize)
		if after != "" {
			builder = builder.WithAfter(after)
		}
		objects, err := builder.Do(ctx)
		if err != nil {
			return nil, 0, err
		}
		if len(objects) > metadataScanPageSize {
			return nil, 0, errors.New("weaviate: native enumeration exceeded its page limit")
		}
		if len(objects) == 0 {
			return selected, dimensions, nil
		}
		for _, object := range objects {
			if object == nil || object.Class != s.className || object.Tenant != "" || len(object.Vectors) != 0 {
				return nil, 0, errors.New("weaviate: native record has an invalid class, tenant or vector shape")
			}
			properties, ok := object.Properties.(map[string]any)
			if !ok {
				return nil, 0, errors.New("weaviate: native properties are not an object")
			}
			doc, facts, decodeErr := decodeDocument(object.ID.String(), properties)
			if decodeErr != nil {
				return nil, 0, decodeErr
			}
			if doc.ID <= after {
				return nil, 0, errors.New("weaviate: native cursor did not advance")
			}
			if err = validateVector(object.Vector, dimensions); err != nil {
				return nil, 0, err
			}
			if dimensions == 0 {
				dimensions = len(object.Vector)
			}
			matched := true
			if predicate != nil {
				values, valuesErr := doc.Metadata.Values()
				if valuesErr != nil {
					return nil, 0, valuesErr
				}
				matched, err = filter.Match(predicate, values)
				if err != nil {
					return nil, 0, err
				}
			}
			if matched {
				selected = append(selected, selectedDocument{id: doc.ID, metadataJSON: facts})
			}
			after = doc.ID
		}
	}
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
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
	selected, dimensions, err := s.selectDocuments(ctx, request.Options.Filter)
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
	query, err := nativeVector(vector, dimensions)
	if err != nil {
		return nil, err
	}
	mode := request.Options.EffectiveMode()
	relevance := additionalDistance
	if mode == vectorstore.SearchModeHybrid {
		relevance = additionalScore
	}
	fields := []graphql.Field{{Name: fieldContent}, {Name: fieldMetadata}, {Name: "_additional", Fields: []graphql.Field{{Name: additionalID}, {Name: relevance}, {Name: additionalVector}}}}
	var ids []string
	if request.Options.Filter != nil {
		for _, record := range selected {
			ids = append(ids, record.id)
		}
	}
	groups := [][]string{nil}
	if ids != nil {
		groups = [][]string{ids}
		if mode == vectorstore.SearchModeSemantic {
			groups = slices.Collect(slices.Chunk(ids, metadataScanPageSize))
		}
	}
	var ranked []rankedDocument
	seen := make(map[string]struct{})
	for _, group := range groups {
		builder := s.client.GraphQL().Get().WithClassName(s.className).WithFields(fields...).WithLimit(request.Options.ResultLimit())
		if mode == vectorstore.SearchModeSemantic {
			builder = builder.WithNearVector(s.client.GraphQL().NearVectorArgBuilder().WithVector(models.C11yVector(query)))
		} else {
			hybrid := s.client.GraphQL().HybridArgumentBuilder().WithQuery(request.Query).WithVector(models.C11yVector(query)).WithProperties([]string{fieldContent}).WithFusionType(graphql.RelativeScore)
			if s.hybridAlpha != nil {
				hybrid = hybrid.WithAlpha(*s.hybridAlpha)
			}
			builder = builder.WithHybrid(hybrid)
		}
		if group != nil {
			builder = builder.WithWhere(identitySelection(group))
		}
		native, queryErr := builder.Do(ctx)
		if queryErr != nil {
			return nil, queryErr
		}
		objects, objectsErr := s.resultObjects(native)
		if objectsErr != nil {
			return nil, objectsErr
		}
		if len(objects) > request.Options.ResultLimit() {
			return nil, errors.New("weaviate: native query exceeded its result limit")
		}
		for _, object := range objects {
			additional, ok := object["_additional"].(map[string]any)
			if !ok || len(object) != 3 || len(additional) != 3 {
				return nil, errors.New("weaviate: native query object has an invalid projection shape")
			}
			id, ok := additional[additionalID].(string)
			if !ok {
				return nil, ErrInvalidObjectID
			}
			doc, _, decodeErr := decodeDocument(id, map[string]any{fieldContent: object[fieldContent], fieldMetadata: object[fieldMetadata]})
			if decodeErr != nil {
				return nil, decodeErr
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, errors.New("weaviate: native query repeated an identity")
			}
			seen[id] = struct{}{}
			if vectorErr := graphQLVector(additional[additionalVector], dimensions); vectorErr != nil {
				return nil, vectorErr
			}
			if group != nil {
				if !slices.Contains(group, id) {
					return nil, errors.New("weaviate: hit is outside selected identities")
				}
				values, valuesErr := doc.Metadata.Values()
				if valuesErr != nil {
					return nil, valuesErr
				}
				matched, matchErr := filter.Match(request.Options.Filter, values)
				if matchErr != nil {
					return nil, matchErr
				}
				if !matched {
					return nil, errors.New("weaviate: native hit changed Core membership")
				}
			}
			raw, relevanceErr := graphQLRelevance(additional, mode)
			if relevanceErr != nil {
				return nil, relevanceErr
			}
			score, scoreErr := s.schema.score(raw, mode)
			if scoreErr != nil {
				return nil, scoreErr
			}
			result, resultErr := vectorstore.NewSearchResult(doc, score)
			if resultErr != nil {
				return nil, resultErr
			}
			rank := -raw
			if mode == vectorstore.SearchModeHybrid {
				rank = raw
			}
			ranked = append(ranked, rankedDocument{result: result, rank: rank})
		}
	}
	slices.SortFunc(ranked, func(left, right rankedDocument) int {
		if order := cmp.Compare(right.rank, left.rank); order != 0 {
			return order
		}
		return strings.Compare(left.result.Document.ID, right.result.Document.ID)
	})
	ranked = ranked[:min(len(ranked), request.Options.ResultLimit())]
	response = &vectorstore.SearchResponse{}
	for _, hit := range ranked {
		if hit.result.Score >= request.Options.MinScore {
			response.Results = append(response.Results, hit.result)
		}
	}
	return response, nil
}

func (s *Store) resultObjects(result *models.GraphQLResponse) ([]map[string]any, error) {
	if result == nil {
		return nil, errors.New("weaviate: GraphQL response is nil")
	}
	if len(result.Errors) != 0 {
		return nil, fmt.Errorf("weaviate: GraphQL query error: %v", result.Errors)
	}
	get, ok := result.Data["Get"].(map[string]any)
	if !ok {
		return nil, errors.New("weaviate: GraphQL response lacks Get")
	}
	values, ok := get[s.className].([]any)
	if !ok {
		return nil, errors.New("weaviate: GraphQL response lacks the class array")
	}
	objects := make([]map[string]any, len(values))
	for i, value := range values {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("weaviate: GraphQL result is not an object")
		}
		objects[i] = object
	}
	return objects, nil
}

func identitySelection(ids []string) *filters.WhereBuilder {
	return filters.Where().WithPath([]string{additionalID}).WithOperator(filters.ContainsAny).WithValueText(ids...)
}

func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	selected, _, err := s.selectDocuments(ctx, predicate)
	if err != nil {
		return err
	}
	groups := make(map[string][]string)
	for _, record := range selected {
		groups[record.metadataJSON] = append(groups[record.metadataJSON], record.id)
	}
	for _, facts := range slices.Sorted(maps.Keys(groups)) {
		for ids := range slices.Chunk(groups[facts], metadataScanPageSize) {
			condition := filters.Where().WithOperator(filters.And).WithOperands([]*filters.WhereBuilder{identitySelection(ids), filters.Where().WithPath([]string{fieldMetadata}).WithOperator(filters.Equal).WithValueText(facts)})
			result, deleteErr := s.client.Batch().ObjectsBatchDeleter().WithClassName(s.className).WithWhere(condition).WithDryRun(false).WithOutput("verbose").Do(ctx)
			if deleteErr != nil {
				return deleteErr
			}
			if err = checkConditionalDeletion(result, s.className, ids); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkConditionalDeletion(response *models.BatchDeleteResponse, class string, ids []string) error {
	if response == nil || response.Results == nil || response.Match == nil || response.Match.Class != class || response.DryRun == nil || *response.DryRun {
		return errors.New("weaviate: conditional delete returned an invalid acknowledgment")
	}
	result := response.Results
	if result.Matches < 0 || result.Matches > int64(len(ids)) || result.Successful != result.Matches || result.Failed != 0 || len(result.Objects) != int(result.Matches) {
		return errors.New("weaviate: conditional deletion did not confirm every native match")
	}
	seen := make(map[string]struct{})
	for _, object := range result.Objects {
		if object == nil || object.Status == nil || *object.Status != models.BatchDeleteResponseResultsObjectsItems0StatusSUCCESS || object.Errors != nil || !slices.Contains(ids, object.ID.String()) {
			return errors.New("weaviate: conditional deletion returned an invalid object result")
		}
		if _, duplicate := seen[object.ID.String()]; duplicate {
			return errors.New("weaviate: conditional deletion repeated an identity")
		}
		seen[object.ID.String()] = struct{}{}
	}
	return nil
}

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := validateObjectID(id); err != nil {
			return err
		}
	}
	seen := make(map[string]struct{})
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		if err := s.client.Data().Deleter().WithClassName(s.className).WithID(id).Do(ctx); err != nil {
			if clientErr, ok := errors.AsType[*fault.WeaviateClientError](err); ok && clientErr.StatusCode == http.StatusNotFound {
				continue
			}
			return err
		}
	}
	return nil
}
