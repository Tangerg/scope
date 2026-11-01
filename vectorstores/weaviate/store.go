package weaviate

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/samber/lo"
	weaviateclient "github.com/weaviate/weaviate-go-client/v5/weaviate"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/fault"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/filters"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/graphql"
	"github.com/weaviate/weaviate/entities/models"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter/keyword"
)

var (
	_ vectorstore.Upserter  = (*Store)(nil)
	_ vectorstore.Querier   = (*Store)(nil)
	_ vectorstore.IDDeleter = (*Store)(nil)
)

type Store struct {
	client      *weaviateclient.Client
	space       vectorstore.SpaceID
	className   string
	schema      nativeSchema
	hybridAlpha *float32
}

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	var alpha *float32
	if config.HybridAlpha != nil {
		alpha = new(*config.HybridAlpha)
	}
	store := &Store{client: config.Client, space: config.Space, className: config.ClassName, hybridAlpha: alpha}
	var err error
	store.schema, err = store.readSchema(ctx)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) readSchema(ctx context.Context) (schema nativeSchema, err error) {
	class, err := s.client.Schema().ClassGetter().WithClassName(s.className).Do(ctx)
	if err != nil {
		return schema, err
	}
	err = schema.read(class, s.className, s.space)
	return schema, err
}
func (s *Store) verify(ctx context.Context) error {
	schema, err := s.readSchema(ctx)
	if err != nil {
		return err
	}
	if schema != s.schema {
		return vectorstore.ErrIncompatibleSpace
	}
	return nil
}
func (s *Store) CheckDocuments(ctx context.Context, space vectorstore.SpaceID, docs []*document.Document) error {
	if err := space.Require(s.space); err != nil {
		return err
	}
	if err := vectorstore.ValidateDocuments(docs); err != nil {
		return err
	}
	for _, doc := range docs {
		if doc.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
		if err := validateObjectID(doc.ID); err != nil {
			return err
		}
	}
	return s.verify(ctx)
}
func (s *Store) Upsert(ctx context.Context, b *vectorstore.Batch) error {
	if err := b.ValidateFor(s.space); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	objects := make([]*models.Object, len(b.Records))
	for i, r := range b.Records {
		if r.Document.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
		obj, err := encodeRecord(r.Document, s.className)
		if err != nil {
			return err
		}
		vector, err := nativeVector(r.Values, len(b.Records[0].Values))
		if err != nil {
			return err
		}
		obj.Vector = models.C11yVector(vector)
		objects[i] = obj
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	resp, err := s.client.Batch().ObjectsBatcher().WithObjects(objects...).Do(ctx)
	if err != nil {
		w := &vectorstore.WriteError{Err: err}
		for _, o := range objects {
			w.Unconfirmed = append(w.Unconfirmed, o.ID.String())
		}
		return w
	}
	return checkBatchAcknowledgments(objects, resp)
}
func checkBatchAcknowledgments(objects []*models.Object, responses []models.ObjectsGetResponse) error {
	w := &vectorstore.WriteError{}
	if len(objects) != len(responses) {
		w.Err = fmt.Errorf("weaviate: batch insert returned %d results for %d objects", len(responses), len(objects))
	}
	states := map[string]string{}
	for _, obj := range objects {
		states[obj.ID.String()] = ""
	}
	for _, r := range responses {
		id := r.ID.String()
		previous, requested := states[id]
		if !requested || previous != "" {
			w.Err = errors.Join(w.Err, fmt.Errorf("weaviate: unrequested or repeated object %q", id))
			if requested {
				states[id] = "unknown"
			}
			continue
		}
		state := "unknown"
		switch {
		case r.Result == nil:
			w.Err = errors.Join(w.Err, fmt.Errorf("weaviate: batch insert has no result for object %s", id))
		case r.Result.Status != nil && *r.Result.Status == models.ObjectsGetResponseAO2ResultStatusFAILED:
			state = "rejected"
			if r.Result.Errors != nil {
				w.Err = errors.Join(w.Err, fmt.Errorf("weaviate: batch insert error for object %s: %v", id, r.Result.Errors.Error))
			} else {
				w.Err = errors.Join(w.Err, fmt.Errorf("weaviate: object %s reported status FAILED", id))
			}
		case r.Result.Status != nil && *r.Result.Status == models.ObjectsGetResponseAO2ResultStatusSUCCESS && r.Result.Errors == nil:
			state = "accepted"
		default:
			w.Err = errors.Join(w.Err, fmt.Errorf("weaviate: batch insert for object %s reported status %s", id, lo.FromPtrOr(r.Result.Status, "none")))
		}
		states[id] = state
	}
	for _, obj := range objects {
		id := obj.ID.String()
		switch states[id] {
		case "accepted":
			w.Acknowledged = append(w.Acknowledged, id)
		case "rejected":
			w.Rejected = append(w.Rejected, id)
		default:
			w.Unconfirmed = append(w.Unconfirmed, id)
		}
	}
	if w.Err == nil && len(w.Acknowledged) == len(objects) {
		return nil
	}
	if w.Err == nil {
		w.Err = errors.New("weaviate: batch receipt incomplete")
	}
	return w
}
func (s *Store) prepareQuery(ctx context.Context, space vectorstore.SpaceID, o vectorstore.SearchOptions) (*filters.WhereBuilder, error) {
	if err := space.Require(s.space); err != nil {
		return nil, err
	}
	if err := o.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		return nil, err
	}
	var where *filters.WhereBuilder
	if p := o.Filter; p != nil {
		term, err := keyword.EqualString(p)
		if err != nil {
			return nil, err
		}
		where = filters.Where().WithPath([]string{fieldTerms}).WithOperator(filters.ContainsAny).WithValueText(hex.EncodeToString([]byte(term)))
	}
	return where, s.verify(ctx)
}
func (s *Store) CheckQuery(ctx context.Context, space vectorstore.SpaceID, o vectorstore.SearchOptions) error {
	_, err := s.prepareQuery(ctx, space, o)
	return err
}
func (s *Store) Query(ctx context.Context, request *vectorstore.Query) (response *vectorstore.SearchResponse, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = request.ValidateFor(s.space); err != nil {
		return nil, err
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
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
	query, err := nativeVector(request.Values, len(request.Values))
	if err != nil {
		return nil, err
	}
	where, err := s.prepareQuery(ctx, request.Space, request.Options)
	if err != nil {
		return nil, err
	}
	dimensions := len(query)
	mode := request.Options.EffectiveMode()
	relevance := additionalDistance
	if mode == vectorstore.SearchModeHybrid {
		relevance = additionalScore
	}
	fields := []graphql.Field{{Name: fieldContent}, {Name: fieldMetadata}, {Name: fieldTerms}, {Name: "_additional", Fields: []graphql.Field{{Name: additionalID}, {Name: relevance}, {Name: additionalVector}}}}
	var ranked []rankedDocument
	seen := make(map[string]struct{})

	builder := s.client.GraphQL().Get().WithClassName(s.className).WithFields(fields...).WithLimit(request.Options.ResultLimit())
	if mode == vectorstore.SearchModeSemantic {
		builder = builder.WithNearVector(s.client.GraphQL().NearVectorArgBuilder().WithVector(models.C11yVector(query)))
	} else {
		hybrid := s.client.GraphQL().HybridArgumentBuilder().WithQuery(request.LexicalText).WithVector(models.C11yVector(query)).WithProperties([]string{fieldContent}).WithFusionType(graphql.RelativeScore)
		if s.hybridAlpha != nil {
			hybrid = hybrid.WithAlpha(*s.hybridAlpha)
		}
		builder = builder.WithHybrid(hybrid)
	}
	if where != nil {
		builder = builder.WithWhere(where)
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
		if !ok || len(object) != 4 || len(additional) != 3 {
			return nil, errors.New("weaviate: native query object has an invalid projection shape")
		}
		id, ok := additional[additionalID].(string)
		if !ok {
			return nil, ErrInvalidObjectID
		}
		doc, decodeErr := decodeDocument(id, map[string]any{fieldContent: object[fieldContent], fieldMetadata: object[fieldMetadata], fieldTerms: object[fieldTerms]})
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

func (s *Store) DeleteIDs(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := validateObjectID(id); err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if err := s.verify(ctx); err != nil {
		return err
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
