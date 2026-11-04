package weaviate

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	"slices"
	"strings"

	"github.com/google/uuid"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"

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
	collection  string
	client      *Client
	space       vectorstore.SpaceID
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
	store := &Store{client: config.Client, collection: config.Collection, space: config.Space, hybridAlpha: alpha}
	var err error
	store.schema, err = store.readSchema(ctx)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) readSchema(ctx context.Context) (schema nativeSchema, err error) {
	class, err := s.client.schema(ctx, s.collection)
	if err != nil {
		return schema, err
	}
	err = schema.read(&class, s.collection, s.space)
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
	objects := make([]*pb.BatchObject, len(b.Records))
	for i, r := range b.Records {
		if r.Document.Media != nil {
			return vectorstore.ErrInvalidDocument
		}
		obj, err := encodeRecord(r.Document)
		if err != nil {
			return err
		}
		vector, err := nativeVector(r.Values, len(b.Records[0].Values))
		if err != nil {
			return err
		}
		obj.Collection = s.collection
		obj.Vectors = []*pb.Vectors{packVector(vector)}
		objects[i] = obj
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	ids := make([]string, len(objects))
	for i, obj := range objects {
		ids[i] = obj.Uuid
	}
	resp, err := s.client.rpc.BatchObjects(s.client.callContext(ctx), &pb.BatchObjectsRequest{Objects: objects})
	return batchOutcome(ids, resp, err)
}

// A successful RPC certifies every item except those explicitly rejected by
// position. A missing or contradictory receipt certifies none.
func batchOutcome(ids []string, reply *pb.BatchObjectsReply, cause error) error {
	unknown := func(err error) error {
		return vectorstore.NewWriteError(err, vectorstore.WriteOutcome{Unconfirmed: ids})
	}
	if cause != nil {
		return unknown(cause)
	}
	if reply == nil {
		return unknown(errors.New("weaviate: missing batch receipt"))
	}
	if len(reply.Errors) == 0 {
		return nil
	}
	failed := make(map[int]bool, len(reply.Errors))
	var reasons []error
	for _, f := range reply.Errors {
		if f == nil || f.Index < 0 || int(f.Index) >= len(ids) || failed[int(f.Index)] {
			return unknown(errors.New("weaviate: ambiguous rejected batch position"))
		}
		failed[int(f.Index)] = true
		reasons = append(reasons, fmt.Errorf("weaviate: rejected item %d: %s", f.Index, f.Error))
	}
	var outcome vectorstore.WriteOutcome
	for i, id := range ids {
		if failed[i] {
			outcome.Rejected = append(outcome.Rejected, id)
		} else {
			outcome.Acknowledged = append(outcome.Acknowledged, id)
		}
	}
	return vectorstore.NewWriteError(errors.Join(reasons...), outcome)
}

func (s *Store) prepareQuery(ctx context.Context, space vectorstore.SpaceID, o vectorstore.SearchOptions) (*pb.Filters, error) {
	if err := space.Require(s.space); err != nil {
		return nil, err
	}
	if err := o.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		return nil, err
	}
	if o.ResultLimit() > math.MaxInt32 {
		return nil, errors.New("weaviate: native result limit exceeds int32")
	}
	var where *pb.Filters
	if p := o.Filter; p != nil {
		term, err := keyword.EqualString(p)
		if err != nil {
			return nil, err
		}
		where = &pb.Filters{Target: &pb.FilterTarget{Target: &pb.FilterTarget_Property{Property: fieldTerms}}, Operator: pb.Filters_OPERATOR_CONTAINS_ANY, TestValue: &pb.Filters_ValueTextArray{ValueTextArray: &pb.TextArray{Values: []string{hex.EncodeToString([]byte(term))}}}}
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
	mode := request.Options.Mode
	native, err := s.client.rpc.Search(s.client.callContext(ctx), searchRequest(s.collection, request, query, where, s.hybridAlpha))
	if err != nil {
		return nil, err
	}
	if native == nil || len(native.Results) > request.Options.ResultLimit() {
		return nil, errors.New("weaviate: malformed native result envelope")
	}
	ranked := make([]rankedDocument, 0, len(native.Results))
	seen := make(map[uuid.UUID]bool, len(native.Results))
	for _, hit := range native.Results {
		doc, id, raw, decodeErr := decodeHit(hit, mode, dimensions)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if seen[id] {
			return nil, errors.New("weaviate: duplicate native object ID")
		}
		seen[id] = true
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
		if err := s.client.delete(ctx, s.collection, id); err != nil {
			return err
		}
	}
	return nil
}
