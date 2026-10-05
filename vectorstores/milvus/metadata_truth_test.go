package milvus

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/milvus-io/milvus-proto/go-api/v2/commonpb"
	"github.com/milvus-io/milvus-proto/go-api/v2/milvuspb"
	"github.com/milvus-io/milvus-proto/go-api/v2/schemapb"
	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestMetadataConditionsResolveBeforeNegation(t *testing.T) {
	for _, source := range []string{`not (active == false)`, `active != false`, `not (n < 1)`, `not (value in ('x', 'y'))`, `not (tags has 'x')`, `not (name like '%')`} {
		got, err := compileFilterText(source)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, `ARRAY_CONTAINS(metadata_filter["kinds"]`) || strings.Contains(got, `metadata[`) {
			t.Errorf("filter %s has competing native JSON truth: %s", source, got)
		}
	}
}

func TestNullPredicatesUseMetadataPresence(t *testing.T) {
	for _, source := range []string{`value is null`, `value is not null`} {
		got, err := compileFilterText(source)
		if err != nil || !strings.Contains(got, `ARRAY_CONTAINS(metadata_filter["present"], "[\"value\"]")`) {
			t.Errorf("filter %s = %s, error=%v", source, got, err)
		}
	}
}

func TestCollectionRequiresCurrentMetadataProjection(t *testing.T) {
	service := newSchemaService(entity.COSINE)
	service.schema.Fields = service.schema.Fields[:4]
	var embeddings atomic.Int64
	store, err := NewStore(t.Context(), schemaConfig(newCollectionClient(t, service), entity.COSINE, false, &embeddings))
	if !errors.Is(err, ErrSchemaMismatch) || store != nil {
		t.Fatalf("obsolete collection accepted: store=%v error=%v", store, err)
	}
}

func TestLikePatternsKeepCoreCharacters(t *testing.T) {
	for _, pattern := range []string{"_", "世界_", `a\%b`, `plain\path%`} {
		compiler := newVisitor()
		if err := filter.Like("name", pattern).Accept(compiler); err != nil {
			t.Errorf("Core pattern %q cannot be represented: %v", pattern, err)
		}
	}
}

func TestNumericProjectionPreservesCoreValueAndOrder(t *testing.T) {
	values := []string{"-18446744073709551615", "-9223372036854775808", "-1000000", "-1.201", "-1.20", "-1.19", "-1", "-0.1", "-1e-9", "-0", "0", "0.0", "1e-9", "0.8", "1", "1.19", "1.20", "1.201", "1000000", "9007199254740992", "9007199254740993", "9223372036854775808", "9223372036854776000", "18446744073709551615", "1e20", "1e100", "1e-100", "1e-300", "1e308", "1e1000", "1.234567890123456789"}
	for _, literal := range []string{"-9223372036854775808", "-1.2", "0", "0.8", "1.2", "1000000.0", "9007199254740993", "9223372036854775808", "9223372036854775808.0", "18446744073709551615", "1e100", "1e-300"} {
		for _, operator := range []string{"==", "<", "<=", ">", ">="} {
			predicate, err := filter.Parse("n " + operator + " " + literal)
			if err != nil {
				t.Fatal(err)
			}
			compiler := newVisitor()
			if err = predicate.Accept(compiler); err != nil {
				t.Fatal(err)
			}
			_, encoded, found := strings.Cut(compiler.snapshot(), " "+operator+" ")
			if !found {
				t.Fatalf("missing native comparison: %s", compiler.snapshot())
			}
			var target string
			quoted, quoteErr := strconv.QuotedPrefix(encoded)
			if quoteErr != nil {
				t.Fatal(quoteErr)
			}
			if err = jsonv2.Unmarshal([]byte(quoted), &target); err != nil {
				t.Fatal(err)
			}
			for _, value := range values {
				attributes := metadata.Map{"n": json.RawMessage(value)}
				_, rawProjection, projectionErr := projectMetadata(attributes)
				if projectionErr != nil {
					t.Fatal(projectionErr)
				}
				var projection metadataProjection
				if decodeErr := jsonv2.Unmarshal(rawProjection, &projection); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				order := strings.Compare(projection.Scalars[`["n"]`], target)
				got := map[string]bool{"==": order == 0, "<": order < 0, "<=": order <= 0, ">": order > 0, ">=": order >= 0}[operator]
				want, matchErr := filter.Match(predicate, map[string]any{"n": json.Number(value)})
				if matchErr != nil || got != want {
					t.Fatalf("%s %s %s: projection=%t Core=%t error=%v", value, operator, literal, got, want, matchErr)
				}
			}
		}
	}
}

func TestIndexRejectsNumbersOutsideCoreRangeBeforeEffects(t *testing.T) {
	for _, number := range []string{"1e1000001", "-1e-1000001", "0e9223372036854775808"} {
		t.Run(number, func(t *testing.T) {
			attributes := metadata.Map{"n": json.RawMessage(number)}
			client := &countingCollection{upserted: 2}
			store := upsertStore(t, client)
			var embeddings atomic.Int64
			modelClient, err := embeddingclient.New(embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
				embeddings.Add(1)
				return nil, errors.New("unexpected embedding")
			}))
			if err != nil {
				t.Fatal(err)
			}
			store.embeddingClient = modelClient
			request := &vectorstore.IndexRequest{Documents: []*document.Document{
				{ID: "first", Text: "valid", Metadata: metadata.Map{}},
				{ID: "second", Text: "invalid", Metadata: attributes},
			}}
			indexErr := store.Index(t.Context(), request)
			if indexErr == nil || !strings.Contains(indexErr.Error(), "outside Core's numeric range") || embeddings.Load() != 0 || client.calls != 0 {
				t.Fatalf("Index error=%v embeddings=%d upserts=%d", indexErr, embeddings.Load(), client.calls)
			}
		})
	}
}

func TestLikeProjectionPreservesCoreMatching(t *testing.T) {
	patterns := []string{"", "%", "_", "a%b", "a_b", "%foo%", "世界_", `a\%b`, `a[.]b`, "quote\"%", "line\n%", "%_%"}
	values := []string{"", "foo", "prefixfoosuffix", "a\nb", "a\nb\n", "a世界b", "世界人", "world", `a\pathb`, "a[.]b", "quote\"value", "line\nvalue", "FOO", "Foo", "🙂", "a🙂b"}
	for _, pattern := range patterns {
		predicate := filter.Like("value", pattern)
		compiler := newVisitor()
		if err := predicate.Accept(compiler); err != nil {
			t.Fatal(err)
		}
		_, encoded, wildcard := strings.Cut(compiler.snapshot(), " like ")
		if !wildcard {
			_, encoded, _ = strings.Cut(compiler.snapshot(), " == ")
		}
		var native string
		quoted, quoteErr := strconv.QuotedPrefix(encoded)
		if quoteErr != nil {
			t.Fatal(quoteErr)
		}
		if err := jsonv2.Unmarshal([]byte(quoted), &native); err != nil {
			t.Fatal(err)
		}
		var matcher *regexp.Regexp
		if wildcard {
			literal := regexp.QuoteMeta(native)
			matcher = regexp.MustCompile("^" + strings.NewReplacer("%", ".*", "_", ".").Replace(literal) + "$")
		}
		for _, value := range values {
			attributes, err := metadata.FromValues(map[string]any{"value": value})
			if err != nil {
				t.Fatal(err)
			}
			_, raw, err := projectMetadata(attributes)
			if err != nil {
				t.Fatal(err)
			}
			var projection metadataProjection
			if err = jsonv2.Unmarshal(raw, &projection); err != nil {
				t.Fatal(err)
			}
			stored := projection.Scalars[`["value"]`]
			got := stored == native
			if wildcard {
				got = matcher.MatchString(stored)
			}
			want, matchErr := filter.Match(predicate, map[string]any{"value": value})
			if matchErr != nil || got != want {
				t.Fatalf("pattern %q value %q: projection=%t Core=%t error=%v", pattern, value, got, want, matchErr)
			}
		}
	}
}

type metadataService struct {
	*schemaService
	fields  map[string]*schemapb.FieldData
	corrupt []byte
}

func (m *metadataService) Upsert(_ context.Context, request *milvuspb.UpsertRequest) (*milvuspb.MutationResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fields = make(map[string]*schemapb.FieldData, len(request.FieldsData))
	for _, field := range request.FieldsData {
		m.fields[field.FieldName] = field
	}
	return &milvuspb.MutationResult{Status: &commonpb.Status{}, UpsertCnt: int64(request.NumRows)}, nil
}

func (m *metadataService) Search(ctx context.Context, request *milvuspb.SearchRequest) (*milvuspb.SearchResults, error) {
	response, err := m.schemaService.Search(ctx, request)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var rows [][]byte
	if m.corrupt != nil {
		rows = [][]byte{m.corrupt, m.corrupt}
	} else {
		rows = m.fields["metadata"].GetScalars().GetJsonData().Data
	}
	for index, field := range response.Results.FieldsData {
		if field.FieldName == "metadata" {
			response.Results.FieldsData[index] = column.NewColumnJSONBytes("metadata", rows).FieldData()
		}
	}
	return response, nil
}

func TestIndexReplacesMetadataProjectionInTheSameNativeWrite(t *testing.T) {
	service := &metadataService{schemaService: newSchemaService(entity.COSINE)}
	service.searchScore = []float32{1, 1}
	var embeddings atomic.Int64
	store, err := NewStore(t.Context(), schemaConfig(newCollectionClient(t, service), entity.COSINE, false, &embeddings))
	if err != nil {
		t.Fatal(err)
	}
	attributes, err := metadata.FromValues(map[string]any{"active": false, "null": nil, "empty": []any{}, "object": map[string]any{}, "array": []any{json.Number("18446744073709551615"), "世界", nil}, "mapping": map[string]any{"0": false}})
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []metadata.Map{attributes, nil} {
		if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "first", Text: "first result", Metadata: current}, {ID: "second", Text: "second result", Metadata: current}}}); err != nil {
			t.Fatal(err)
		}
		service.mu.Lock()
		fields := maps.Clone(service.fields)
		service.mu.Unlock()
		if names := slices.Sorted(maps.Keys(fields)); !slices.Equal(names, []string{"content", "id", "metadata", "metadata_filter", "vector"}) {
			t.Fatalf("atomic native columns = %v", names)
		}
		for index, raw := range fields["metadata"].GetScalars().GetJsonData().Data {
			var original metadata.Map
			if err = jsonv2.Unmarshal(raw, &original); err != nil || original == nil {
				t.Fatalf("metadata object=%s error=%v", raw, err)
			}
			if !maps.EqualFunc(original, current, func(left, right json.RawMessage) bool { return string(left) == string(right) }) {
				t.Fatalf("metadata=%v want=%v", original, current)
			}
			var projection metadataProjection
			if err = jsonv2.Unmarshal(fields["metadata_filter"].GetScalars().GetJsonData().Data[index], &projection); err != nil {
				t.Fatal(err)
			}
			if current == nil {
				if len(projection.Present) != 0 || len(projection.Scalars) != 0 || len(projection.Members) != 0 {
					t.Fatalf("obsolete projection survived: %+v", projection)
				}
			} else {
				want := []string{`["active"]`, `["array",0]`, `["array",1]`, `["array"]`, `["empty"]`, `["mapping","0"]`, `["mapping"]`, `["object"]`}
				if !slices.Equal(projection.Present, want) || projection.Scalars[`["array",0]`] != "number:20922337203685477582718446744073709551615/" {
					t.Fatalf("metadata projection=%+v", projection)
				}
			}
		}
		response, searchErr := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "text"})
		if searchErr != nil || response == nil || len(response.Results) != 2 {
			t.Fatalf("Search=%v error=%v", response, searchErr)
		}
		if !maps.EqualFunc(response.Results[0].Document.Metadata, current, func(left, right json.RawMessage) bool { return string(left) == string(right) }) {
			t.Fatalf("returned metadata=%v want=%v", response.Results[0].Document.Metadata, current)
		}
	}
}

func TestNilMetadataObjectsAreRefusedOnRead(t *testing.T) {
	for _, raw := range []string{"null", "[]", `"wrong"`, "{broken"} {
		t.Run(strconv.Quote(raw), func(t *testing.T) {
			service := &metadataService{schemaService: newSchemaService(entity.COSINE), corrupt: []byte(raw)}
			service.searchScore = []float32{1, 1}
			var embeddings atomic.Int64
			store, err := NewStore(t.Context(), schemaConfig(newCollectionClient(t, service), entity.COSINE, false, &embeddings))
			if err != nil {
				t.Fatal(err)
			}
			if response, searchErr := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "text"}); searchErr == nil || response != nil {
				t.Fatalf("obsolete metadata accepted: response=%v error=%v", response, searchErr)
			}
		})
	}
}

func TestNativeResultDecodingErrorRemainsTheCause(t *testing.T) {
	want := errors.New("native metadata column decoding failed")
	store := &Store{metricType: entity.COSINE}
	results, err := store.buildDocumentsFromResults(milvusclient.ResultSet{Err: want}, 0)
	if results != nil || !errors.Is(err, want) {
		t.Fatalf("result=%v error=%v want cause=%v", results, err, want)
	}
}
