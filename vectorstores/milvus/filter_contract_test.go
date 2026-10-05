package milvus

import (
	"context"
	jsonv2 "encoding/json/v2"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/milvus-io/milvus-proto/go-api/v2/commonpb"
	"github.com/milvus-io/milvus-proto/go-api/v2/milvuspb"
	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/pkg/v2/util/merr"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type filterService struct {
	*schemaService
	expected string
	upserts  atomic.Int64
	deletes  atomic.Int64
	rows     [][]byte
	test     *testing.T
}

func (f *filterService) Upsert(ctx context.Context, request *milvuspb.UpsertRequest) (*milvuspb.MutationResult, error) {
	f.upserts.Add(1)
	var ids []string
	var content []string
	var rows [][]byte
	for _, field := range request.FieldsData {
		switch field.FieldName {
		case "id":
			ids = field.GetScalars().GetStringData().Data
		case "content":
			content = field.GetScalars().GetStringData().Data
		case "metadata":
			rows = field.GetScalars().GetJsonData().Data
		}
	}
	if !slices.Equal(ids, []string{"first", "second"}) || !slices.Equal(content, []string{"first result", "second result"}) || len(rows) != 2 {
		f.test.Errorf("native upsert IDs=%v, content=%v, metadata=%q", ids, content, rows)
	}
	for _, raw := range rows {
		var values map[string]any
		if err := jsonv2.Unmarshal(raw, &values); err != nil {
			f.test.Error(err)
		} else if values["id"] != "meta-id" || values["content"] != "meta-content" || values["author"] != "Alice" {
			f.test.Errorf("native metadata = %#v", values)
		}
	}
	f.mu.Lock()
	f.rows = rows
	f.mu.Unlock()
	return &milvuspb.MutationResult{Status: &commonpb.Status{}, UpsertCnt: int64(request.NumRows)}, nil
}

func (f *filterService) Search(ctx context.Context, request *milvuspb.SearchRequest) (*milvuspb.SearchResults, error) {
	if request.Dsl != f.expected {
		return &milvuspb.SearchResults{Status: merr.Status(merr.WrapErrParameterInvalidMsg(
			"filter must address the metadata JSON column: %s", request.Dsl))}, nil
	}
	response, err := f.schemaService.Search(ctx, request)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for index, field := range response.Results.FieldsData {
		if field.FieldName == "metadata" {
			response.Results.FieldsData[index] = column.NewColumnJSONBytes("metadata", f.rows).FieldData()
		}
	}
	return response, nil
}

func (f *filterService) Delete(ctx context.Context, request *milvuspb.DeleteRequest) (*milvuspb.MutationResult, error) {
	call := f.deletes.Add(1)
	want := f.expected
	if call == 2 {
		want = `id in ["first"]`
	}
	if request.Expr != want {
		return &milvuspb.MutationResult{Status: merr.Status(merr.WrapErrParameterInvalidMsg(
			"unexpected deletion selector: %s", request.Expr))}, nil
	}
	return &milvuspb.MutationResult{Status: &commonpb.Status{}, DeleteCnt: 1}, nil
}

func TestFiltersSelectIndexedMetadataThroughNativeSDK(t *testing.T) {
	values, err := metadata.FromValues(map[string]any{
		"author": "Alice", "id": "meta-id", "content": "meta-content", "vector": 7,
		"metadata": map[string]any{"version": 2}, "tags": []string{"rag"},
		"profile": map[string]any{"a.b": "literal", "items": []any{map[string]any{"name": "Alice"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct{ source, native string }{
		{`author == 'Alice'`, `metadata["author"] == "Alice"`},
		{`id == 'meta-id'`, `metadata["id"] == "meta-id"`},
		{`content == 'meta-content'`, `metadata["content"] == "meta-content"`},
		{`vector == 7`, `metadata["vector"] == 7`},
		{`metadata['version'] == 2`, `metadata["metadata"]["version"] == 2`},
		{`profile['a.b'] == 'literal'`, `metadata["profile"]["a.b"] == "literal"`},
		{`profile['items'][0]['name'] == 'Alice'`, `metadata["profile"]["items"][0]["name"] == "Alice"`},
		{`tags has 'rag'`, `ARRAY_CONTAINS(metadata["tags"], "rag")`},
		{`author like 'A%'`, `metadata["author"] like "A%"`},
		{`author like 'Alice'`, `metadata["author"] == "Alice"`},
	} {
		t.Run(sample.source, func(t *testing.T) {
			service := &filterService{schemaService: newSchemaService(entity.COSINE), expected: sample.native, test: t}
			service.searchScore = []float32{1, 1}
			var embeddings atomic.Int64
			store, err := NewStore(t.Context(), schemaConfig(newCollectionClient(t, service), entity.COSINE, false, &embeddings))
			if err != nil {
				t.Fatal(err)
			}
			indexRequest, err := vectorstore.NewIndexRequest([]*document.Document{
				{ID: "first", Text: "first result", Metadata: values},
				{ID: "second", Text: "second result", Metadata: values},
			})
			if err != nil {
				t.Fatal(err)
			}
			if indexErr := store.Index(t.Context(), indexRequest); indexErr != nil {
				t.Fatal(indexErr)
			}
			predicate, err := filter.Parse(sample.source)
			if err != nil {
				t.Fatal(err)
			}
			search, err := vectorstore.NewSearchRequest("text")
			if err != nil {
				t.Fatal(err)
			}
			search.Options.Filter = predicate
			response, err := store.Search(t.Context(), search)
			if err != nil || response == nil || len(response.Results) != 2 {
				t.Fatalf("Search = %#v, %v", response, err)
			}
			for index, result := range response.Results {
				if want := indexRequest.Documents[index]; result.Document.ID != want.ID || result.Document.Text != want.Text {
					t.Fatalf("result document = %#v, want %#v", result.Document, want)
				}
				returned, returnedErr := result.Document.Metadata.Values()
				if returnedErr != nil || returned["id"] != "meta-id" || returned["content"] != "meta-content" {
					t.Fatalf("result metadata = %#v, %v", returned, returnedErr)
				}
			}
			if err := store.DeleteWhere(t.Context(), predicate); err != nil {
				t.Fatal(err)
			}
			if err := store.DeleteIDs(t.Context(), []string{"first"}); err != nil {
				t.Fatal(err)
			}
			if service.upserts.Load() != 1 || service.deletes.Load() != 2 {
				t.Fatalf("upserts=%d, deletes=%d", service.upserts.Load(), service.deletes.Load())
			}
		})
	}
}

func TestDeleteIDsPreservesLiteralPrimaryKeysThroughNativeSDK(t *testing.T) {
	for _, sample := range []struct {
		name   string
		ids    []string
		native string
	}{
		{"quotes, backslashes, and newlines", []string{`quote"id`, `path\id`, "line\nid", "世界"},
			`id in ["quote\"id","path\\id","line\nid","世界"]`},
		{"query syntax inside an ID", []string{`a" ] or true or id in ["x`},
			`id in ["a\" ] or true or id in [\"x"]`},
	} {
		t.Run(sample.name, func(t *testing.T) {
			service := &filterService{schemaService: newSchemaService(entity.COSINE), expected: sample.native, test: t}
			var embeddings atomic.Int64
			store, err := NewStore(t.Context(), schemaConfig(newCollectionClient(t, service), entity.COSINE, false, &embeddings))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.DeleteIDs(t.Context(), sample.ids); err != nil {
				t.Fatal(err)
			}
			if service.deletes.Load() != 1 || embeddings.Load() != 0 {
				t.Fatalf("deletes=%d, embeddings=%d", service.deletes.Load(), embeddings.Load())
			}
		})
	}
}

func TestUnrepresentableLikePatternsFailBeforeExternalIO(t *testing.T) {
	for _, pattern := range []string{"_", "世界_", `a\%b`, `plain\path%`} {
		t.Run(pattern, func(t *testing.T) {
			predicate := filter.Like("author", pattern)
			if err := predicate.Accept(newVisitor()); err == nil {
				t.Error("compiler accepted a pattern that changes Core semantics")
			}
			service := &filterService{schemaService: newSchemaService(entity.COSINE), test: t}
			var embeddings atomic.Int64
			store, err := NewStore(t.Context(), schemaConfig(newCollectionClient(t, service), entity.COSINE, false, &embeddings))
			if err != nil {
				t.Fatal(err)
			}
			search, err := vectorstore.NewSearchRequest("text")
			if err != nil {
				t.Fatal(err)
			}
			search.Options.Filter = predicate
			response, err := store.Search(t.Context(), search)
			if err == nil || response != nil || !strings.Contains(err.Error(), "Core semantics") {
				t.Fatalf("Search = %#v, %v; want unsupported pattern", response, err)
			}
			if err := store.DeleteWhere(t.Context(), predicate); err == nil || !strings.Contains(err.Error(), "Core semantics") {
				t.Fatalf("DeleteWhere = %v; want unsupported pattern", err)
			}
			if service.deletes.Load() != 0 || embeddings.Load() != 0 {
				t.Fatalf("unsupported filter caused %d deletions and %d embeddings", service.deletes.Load(), embeddings.Load())
			}
		})
	}
}

var _ milvuspb.MilvusServiceServer = (*filterService)(nil)
