package milvus

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/milvus-io/milvus-proto/go-api/v2/commonpb"
	"github.com/milvus-io/milvus-proto/go-api/v2/milvuspb"
	"github.com/milvus-io/milvus-proto/go-api/v2/schemapb"
	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const (
	nameTypeFailure   = `(ARRAY_CONTAINS(metadata_filter["present"], "[\"name\"]") and not (ARRAY_CONTAINS(metadata_filter["kinds"]["string"], "[\"name\"]")))`
	numberTypeFailure = `(ARRAY_CONTAINS(metadata_filter["present"], "[\"n\"]") and not (ARRAY_CONTAINS(metadata_filter["kinds"]["number"], "[\"n\"]")))`
	activeCondition   = `(ARRAY_CONTAINS(metadata_filter["kinds"]["boolean"], "[\"active\"]") and metadata_filter["scalars"]["[\"active\"]"] == "boolean:true")`
	nameCondition     = `(ARRAY_CONTAINS(metadata_filter["kinds"]["string"], "[\"name\"]") and metadata_filter["scalars"]["[\"name\"]"] like "string:%")`
)

func TestFilterFailureConditionsPreserveCoreShortCircuit(t *testing.T) {
	for _, sample := range []struct{ source, invalid string }{
		{`name like '%'`, nameTypeFailure},
		{`not (name like '%')`, nameTypeFailure},
		{`n < 1`, numberTypeFailure},
		{`not (n < 1)`, numberTypeFailure},
		{`n != 1`, ""},
		{`tags has 'x'`, ""},
		{`active == true and name like '%'`, "(" + activeCondition + ") and (" + nameTypeFailure + ")"},
		{`active == true or name like '%'`, "(not (" + activeCondition + ")) and (" + nameTypeFailure + ")"},
		{`name like '%' and active == true`, nameTypeFailure},
		{`name like '%' or active == true`, nameTypeFailure},
		{`name like '%' and n < 1`, "(" + nameTypeFailure + ") or ((" + nameCondition + ") and (" + numberTypeFailure + "))"},
		{`name like '%' or n < 1`, "(" + nameTypeFailure + ") or ((not (" + nameCondition + ")) and (" + numberTypeFailure + "))"},
	} {
		t.Run(sample.source, func(t *testing.T) {
			predicate, err := filter.Parse(sample.source)
			if err != nil {
				t.Fatal(err)
			}
			compiler := newVisitor()
			if err = predicate.Accept(compiler); err != nil {
				t.Fatal(err)
			}
			if compiler.invalid() != sample.invalid {
				t.Fatalf("failure condition=%s want=%s", compiler.invalid(), sample.invalid)
			}
			if sample.invalid != "" && !strings.HasSuffix(compiler.snapshot(), "and not ("+sample.invalid+")") {
				t.Fatalf("failing rows can enter selection: %s", compiler.snapshot())
			}
		})
	}
}

type filterTypeService struct {
	*schemaService
	test           *testing.T
	expected       string
	expectedSearch string
	response       *milvuspb.QueryResults
	failure        error
	queried        atomic.Int64
	deleted        atomic.Int64
}

func (f *filterTypeService) Query(ctx context.Context, request *milvuspb.QueryRequest) (*milvuspb.QueryResults, error) {
	f.queried.Add(1)
	if request.Expr != f.expected || request.CollectionName != "documents" || !slices.Equal(request.OutputFields, []string{"id", "metadata"}) {
		f.test.Errorf("filter validation request=%v", request)
	}
	if len(request.QueryParams) != 1 || request.QueryParams[0].Key != "limit" || request.QueryParams[0].Value != "1" {
		f.test.Errorf("unbounded filter validation: %v", request.QueryParams)
	}
	if f.failure != nil || f.response != nil {
		return f.response, f.failure
	}
	return f.schemaService.Query(ctx, request)
}

func (f *filterTypeService) Search(ctx context.Context, request *milvuspb.SearchRequest) (*milvuspb.SearchResults, error) {
	if request.Dsl != f.expectedSearch {
		f.test.Errorf("search selector=%s want=%s", request.Dsl, f.expectedSearch)
	}
	return f.schemaService.Search(ctx, request)
}

func (f *filterTypeService) Delete(_ context.Context, request *milvuspb.DeleteRequest) (*milvuspb.MutationResult, error) {
	f.deleted.Add(1)
	if request.Expr != f.expectedSearch {
		f.test.Errorf("deletion selector=%s want=%s", request.Expr, f.expectedSearch)
	}
	return &milvuspb.MutationResult{Status: &commonpb.Status{}, DeleteCnt: 1}, nil
}

func filterValidationRow(id string, raw []byte) *milvuspb.QueryResults {
	return &milvuspb.QueryResults{Status: &commonpb.Status{}, OutputFields: []string{"id", "metadata"}, FieldsData: []*schemapb.FieldData{
		column.NewColumnVarChar("id", []string{id}).FieldData(),
		column.NewColumnJSONBytes("metadata", [][]byte{raw}).FieldData(),
	}}
}

func TestFilterTypeErrorsStopSearchAndDeletionBeforeEffects(t *testing.T) {
	for _, sample := range []struct{ source, raw, invalid string }{
		{`n < 1`, `{"n":"wrong"}`, numberTypeFailure},
		{`not (n < 1)`, `{"n":[]}`, numberTypeFailure},
		{`name like '%'`, `{"name":false}`, nameTypeFailure},
		{`not (name like '%')`, `{"name":{}}`, nameTypeFailure},
	} {
		t.Run(sample.source, func(t *testing.T) {
			predicate, err := filter.Parse(sample.source)
			if err != nil {
				t.Fatal(err)
			}
			attributes, err := decodeMetadata([]byte(sample.raw))
			if err != nil {
				t.Fatal(err)
			}
			values, err := attributes.Values()
			if err != nil {
				t.Fatal(err)
			}
			_, wantErr := filter.Match(predicate, values)
			if wantErr == nil {
				t.Fatal("fixture does not violate the Core contract")
			}
			service := &filterTypeService{schemaService: newSchemaService(entity.COSINE), test: t, expected: sample.invalid, response: filterValidationRow("bad", []byte(sample.raw))}
			var embeddings atomic.Int64
			store, err := NewStore(t.Context(), schemaConfig(newCollectionClient(t, service), entity.COSINE, false, &embeddings))
			if err != nil {
				t.Fatal(err)
			}
			request := &vectorstore.SearchRequest{Query: "text", Options: vectorstore.SearchOptions{Filter: predicate}}
			response, searchErr := store.Search(t.Context(), request)
			if response != nil || searchErr == nil || !strings.Contains(searchErr.Error(), wantErr.Error()) {
				t.Fatalf("Search=%v error=%v want=%v", response, searchErr, wantErr)
			}
			if deleteErr := store.DeleteWhere(t.Context(), predicate); deleteErr == nil || !strings.Contains(deleteErr.Error(), wantErr.Error()) {
				t.Fatalf("DeleteWhere error=%v want=%v", deleteErr, wantErr)
			}
			if embeddings.Load() != 0 || service.searched != 0 || service.deleted.Load() != 0 || service.queried.Load() != 2 {
				t.Fatalf("effects: embeddings=%d search=%d delete=%d query=%d", embeddings.Load(), service.searched, service.deleted.Load(), service.queried.Load())
			}
		})
	}
}

func TestFilterValidationFailuresRemainFailures(t *testing.T) {
	for _, sample := range []struct {
		name     string
		response *milvuspb.QueryResults
		failure  error
		message  string
	}{
		{"transport", nil, status.Error(codes.Internal, "validation transport failed"), "validation transport failed"},
		{"native", &milvuspb.QueryResults{Status: &commonpb.Status{Code: 65535, Reason: "validation server failed"}}, nil, "validation server failed"},
		{"null metadata", filterValidationRow("bad", []byte(`null`)), nil, "metadata must be an object"},
		{"malformed metadata", filterValidationRow("bad", []byte(`{broken`)), nil, "decode filter validation metadata"},
		{"empty ID", filterValidationRow("", []byte(`{"name":false}`)), nil, "empty document ID"},
		{"missing metadata", &milvuspb.QueryResults{Status: &commonpb.Status{}, OutputFields: []string{"id"}, FieldsData: []*schemapb.FieldData{column.NewColumnVarChar("id", []string{"bad"}).FieldData()}}, nil, "one complete document"},
		{"inconsistent projection", filterValidationRow("bad", []byte(`{"name":"valid"}`)), nil, "projection is inconsistent"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			service := &filterTypeService{schemaService: newSchemaService(entity.COSINE), test: t, expected: nameTypeFailure, response: sample.response, failure: sample.failure}
			var embeddings atomic.Int64
			store, err := NewStore(t.Context(), schemaConfig(newCollectionClient(t, service), entity.COSINE, false, &embeddings))
			if err != nil {
				t.Fatal(err)
			}
			predicate := filter.Like("name", "%")
			response, searchErr := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "text", Options: vectorstore.SearchOptions{Filter: predicate}})
			if response != nil || searchErr == nil || !strings.Contains(searchErr.Error(), sample.message) {
				t.Fatalf("Search=%v error=%v", response, searchErr)
			}
			if deleteErr := store.DeleteWhere(t.Context(), predicate); deleteErr == nil || !strings.Contains(deleteErr.Error(), sample.message) {
				t.Fatalf("DeleteWhere error=%v", deleteErr)
			}
			if embeddings.Load() != 0 || service.searched != 0 || service.deleted.Load() != 0 {
				t.Fatal("validation failure reached an external effect")
			}
		})
	}
}

func TestFilterValidationKeepsMissingNullAndNonArrayHasSemantics(t *testing.T) {
	for _, source := range []string{`not (name like '%')`, `not (n < 1)`, `not (tags has 'x')`} {
		t.Run(source, func(t *testing.T) {
			predicate, err := filter.Parse(source)
			if err != nil {
				t.Fatal(err)
			}
			for _, values := range []map[string]any{{}, {"name": nil, "n": nil}, {"tags": "scalar"}, {"tags": map[string]any{}}} {
				if match, matchErr := filter.Match(predicate, values); matchErr != nil || !match {
					t.Fatalf("metadata=%v match=%t error=%v", values, match, matchErr)
				}
				attributes, attributesErr := metadata.FromValues(values)
				if attributesErr != nil {
					t.Fatal(attributesErr)
				}
				if _, _, projectionErr := projectMetadata(attributes); projectionErr != nil {
					t.Fatal(projectionErr)
				}
			}
			compiler := newVisitor()
			if err = predicate.Accept(compiler); err != nil {
				t.Fatal(err)
			}
			service := &filterTypeService{schemaService: newSchemaService(entity.COSINE), test: t, expected: compiler.invalid(), expectedSearch: compiler.snapshot()}
			service.searchScore = []float32{1, 1}
			var embeddings atomic.Int64
			store, err := NewStore(t.Context(), schemaConfig(newCollectionClient(t, service), entity.COSINE, false, &embeddings))
			if err != nil {
				t.Fatal(err)
			}
			if response, searchErr := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "text", Options: vectorstore.SearchOptions{Filter: predicate}}); searchErr != nil || response == nil {
				t.Fatalf("Search=%v error=%v", response, searchErr)
			}
			if deleteErr := store.DeleteWhere(t.Context(), predicate); deleteErr != nil {
				t.Fatal(deleteErr)
			}
			wantQueries := int64(2)
			if source == `not (tags has 'x')` {
				wantQueries = 0
			}
			if embeddings.Load() != 1 || service.deleted.Load() != 1 || service.queried.Load() != wantQueries {
				t.Fatalf("effects: embeddings=%d delete=%d query=%d", embeddings.Load(), service.deleted.Load(), service.queried.Load())
			}
		})
	}
}
