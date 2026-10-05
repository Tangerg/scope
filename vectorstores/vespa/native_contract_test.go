package vespa

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type contractEmbeddingModel struct{ calls atomic.Int32 }

func (c *contractEmbeddingModel) Call(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
	c.calls.Add(1)
	outputs := make([]*embedding.Output, len(request.Texts))
	for index := range outputs {
		outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
	}
	return embedding.NewResponse(outputs, nil)
}

func newNativeContractStore(t *testing.T, config StoreConfig, handler http.HandlerFunc) *Store {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	config.Endpoint = server.URL
	config.HTTPClient = server.Client()
	config.SchemaName = "document"
	config.Namespace = "scope"
	config.RankingProfile = "closeness"
	config.DocumentBatcher = maxHitsBatcher{}
	if config.EmbeddingModel == nil {
		config.EmbeddingModel = maxHitsEmbeddingModel{}
	}
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestStoreRejectsConflictingPhysicalFields(t *testing.T) {
	for _, field := range []string{"same", "scope_namespace", "scope_metadata", "scope_metadata_paths", "documentid", "sddocname", "summaryfeatures", "matchfeatures"} {
		t.Run(field, func(t *testing.T) {
			config := StoreConfig{Endpoint: "http://127.0.0.1:1", SchemaName: "document", RankingProfile: "closeness",
				EmbeddingModel: maxHitsEmbeddingModel{}, DocumentBatcher: maxHitsBatcher{}, ContentField: field, EmbeddingField: field}
			if _, err := NewStore(t.Context(), config); err == nil {
				t.Fatal("conflicting fields accepted")
			}
			if field != "same" {
				config.EmbeddingField = "vector"
				if _, err := NewStore(t.Context(), config); err == nil {
					t.Fatal("reserved content field accepted")
				}
				config.ContentField, config.EmbeddingField = "body", field
				if _, err := NewStore(t.Context(), config); err == nil {
					t.Fatal("reserved embedding field accepted")
				}
			}
		})
	}
}

func TestNativeIdentityAndMetadataRoundTrip(t *testing.T) {
	var requests atomic.Int32
	store := newNativeContractStore(t, StoreConfig{ContentField: "body", EmbeddingField: "vector"}, func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		var body struct {
			Fields  metadata.Map `json:"fields"`
			YQL     string       `json:"yql"`
			Summary string       `json:"presentation.summary"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		switch request.URL.Path {
		case "/document/v1/scope/document/docid/one":
			if got := slices.Sorted(maps.Keys(body.Fields)); !slices.Equal(got, []string{"body", "ordinal", "scope_metadata", "scope_metadata_paths", "scope_namespace", "vector"}) {
				t.Errorf("stored fields = %v", got)
			}
			fmt.Fprint(writer, `{}`)
		case "/search/":
			if body.Summary != "default" {
				t.Errorf("summary = %q, want default", body.Summary)
			}
			if !strings.HasSuffix(body.YQL, ` and (scope_metadata_paths contains "[\"ordinal\"]" and ordinal = 9007199254740993)`) {
				t.Errorf("metadata filter = %s", body.YQL)
			}
			fmt.Fprint(writer, `{"root":{"coverage":{"coverage":100,"full":true},"children":[{"id":"id:scope:document::one","relevance":0.8,"fields":{"documentid":"id:scope:document::one","sddocname":"document","summaryfeatures":{"rank":1},"matchfeatures":{"match":2},"scope_namespace":"scope","scope_metadata":"{\"ordinal\":9007199254740993}","body":"hello","vector":{"values":[1,0]},"ordinal":9007199254740993}}]}}`)
		default:
			t.Errorf("unexpected request %s", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	})
	attributes, err := metadata.FromValues(map[string]any{"ordinal": uint64(9007199254740993)})
	if err != nil {
		t.Fatal(err)
	}
	if indexErr := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "hello", Metadata: attributes}}}); indexErr != nil {
		t.Fatal(indexErr)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "hello", Options: vectorstore.SearchOptions{Filter: filter.EQ("ordinal", uint64(9007199254740993))}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != "one" || response.Results[0].Document.Text != "hello" {
		t.Fatalf("search result = %+v", response)
	}
	if got := response.Results[0].Document.Metadata; len(got) != 1 || string(got["ordinal"]) != "9007199254740993" {
		t.Fatalf("metadata = %v, want only the original exact ordinal", got)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
}

func TestIndexAllowsAnOrdinaryDocIDMetadataKey(t *testing.T) {
	store := newNativeContractStore(t, StoreConfig{}, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/search/" {
			var body struct {
				YQL string `json:"yql"`
			}
			if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
				t.Error(err)
			}
			if !strings.HasSuffix(body.YQL, ` and (scope_metadata_paths contains "[\"doc_id\"]" and doc_id contains "metadata value")`) {
				t.Errorf("metadata filter = %s", body.YQL)
			}
			fmt.Fprint(writer, `{"root":{"coverage":{"coverage":100,"full":true},"children":[{"id":"id:scope:document::native identity","relevance":0.8,"fields":{"content":"text","scope_metadata":"{\"doc_id\":\"metadata value\"}","doc_id":"metadata value"}}]}}`)
			return
		}
		var body struct {
			Fields metadata.Map `json:"fields"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
		}
		if got := string(body.Fields["doc_id"]); got != `"metadata value"` {
			t.Errorf("doc_id metadata = %s", got)
		}
		fmt.Fprint(writer, `{}`)
	})
	attributes, err := metadata.FromValues(map[string]any{"doc_id": "metadata value"})
	if err != nil {
		t.Fatal(err)
	}
	if indexErr := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "native identity", Text: "text", Metadata: attributes}}}); indexErr != nil {
		t.Fatal(indexErr)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "text", Options: vectorstore.SearchOptions{Filter: filter.EQ("doc_id", "metadata value")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != "native identity" || string(response.Results[0].Document.Metadata["doc_id"]) != `"metadata value"` {
		t.Fatalf("identity and metadata = %+v", response)
	}
}

func TestReservedMetadataAndFiltersFailBeforeIO(t *testing.T) {
	for _, field := range []string{"body", "vector", "scope_namespace", "scope_metadata", "scope_metadata_paths", "documentid", "sddocname", "summaryfeatures", "matchfeatures"} {
		t.Run(field, func(t *testing.T) {
			model := new(contractEmbeddingModel)
			var calls atomic.Int32
			store := newNativeContractStore(t, StoreConfig{ContentField: "body", EmbeddingField: "vector", EmbeddingModel: model}, func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				fmt.Fprint(writer, `{"root":{"coverage":{"coverage":100,"full":true},"children":[]}}`)
			})
			attributes, err := metadata.FromValues(map[string]any{field: "overwrite"})
			if err != nil {
				t.Fatal(err)
			}
			err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "first", Text: "text"}, {ID: "second", Text: "text", Metadata: attributes}}})
			if err == nil {
				t.Error("reserved metadata accepted")
			}
			for _, predicate := range []filter.Predicate{filter.EQ(field, "value"), filter.EQ(filter.Index(field, "nested"), "value")} {
				if response, searchErr := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate}}); searchErr == nil || response != nil {
					t.Errorf("reserved search: response=%v error=%v", response, searchErr)
				}
				if deleteErr := store.DeleteWhere(t.Context(), predicate); deleteErr == nil {
					t.Error("reserved deletion accepted")
				}
			}
			if calls.Load() != 0 || model.calls.Load() != 0 {
				t.Fatalf("invalid request reached I/O: HTTP=%d embedding=%d", calls.Load(), model.calls.Load())
			}
		})
	}
}

func TestLikeTranslationPreservesCoreMatching(t *testing.T) {
	patterns := []string{"foo", "", "%", "a%b", "a_b", "%foo%", "世界_", `a\%b`, `a[.]b`, "quote\"%", "line\n%"}
	values := []string{"", "foo", "prefixfoosuffix", "a\nb", "a\nb\n", "a世界b", "世界人", "world", `a\pathb`, "a[.]b", "quote\"value", "line\nvalue", "FOO", "Foo"}
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			predicate := filter.Like("value", pattern)
			compiled, err := (&Store{}).buildFilter(predicate)
			if err != nil {
				t.Fatal(err)
			}
			encoded, ok := strings.CutPrefix(compiled, `(scope_metadata_paths contains "[\"value\"]" and value matches `)
			if !ok {
				t.Fatalf("native expression = %s", compiled)
			}
			var expression string
			if decodeErr := jsonv2.Unmarshal([]byte(strings.TrimSuffix(encoded, ")")), &expression); decodeErr != nil {
				t.Fatalf("native string literal = %s: %v", encoded, decodeErr)
			}
			matcher, err := regexp.Compile(expression)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range values {
				want, matchErr := filter.Match(predicate, map[string]any{"value": value})
				if matchErr != nil {
					t.Fatal(matchErr)
				}
				if got := matcher.MatchString(value); got != want {
					t.Errorf("pattern %q value %q: native regexp=%t Core=%t", pattern, value, got, want)
				}
			}
		})
	}
}

func TestNativeComparisonsUseSupportedOperators(t *testing.T) {
	for _, sample := range []struct{ source, want string }{
		{`value != 1`, `!((scope_metadata_paths contains "[\"value\"]" and value = 1))`},
		{`value != true`, `!((scope_metadata_paths contains "[\"value\"]" and value = true))`},
		{`value in (true, false)`, `(scope_metadata_paths contains "[\"value\"]" and (value = true or value = false))`},
		{`value in (1.2, 2.3)`, `(scope_metadata_paths contains "[\"value\"]" and (value = 1.2 or value = 2.3))`},
		{`value in ('Alice', 'Bob')`, `(scope_metadata_paths contains "[\"value\"]" and (value contains "Alice" or value contains "Bob"))`},
	} {
		t.Run(sample.source, func(t *testing.T) {
			predicate, err := filter.Parse(sample.source)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (&Store{}).buildFilter(predicate)
			if err != nil || got != sample.want {
				t.Fatalf("native expression = %q, error = %v, want %q", got, err, sample.want)
			}
		})
	}
}

func TestStringLiteralsRoundTripNativeJSONEscapes(t *testing.T) {
	for _, value := range []string{`path\name`, "quote\"text", "line\nvalue", "tab\tvalue", "世界", `x\" or true or value contains "y`} {
		compiled, err := (&Store{}).buildFilter(filter.EQ("value", value))
		if err != nil {
			t.Fatal(err)
		}
		encoded := strings.TrimSuffix(strings.TrimPrefix(compiled, `(scope_metadata_paths contains "[\"value\"]" and value contains `), ")")
		var got string
		if err := jsonv2.Unmarshal([]byte(encoded), &got); err != nil || got != value {
			t.Errorf("literal %q: encoded=%s decoded=%q error=%v", value, encoded, got, err)
		}
	}
}

func TestUnrepresentableIntegersFailBeforeIO(t *testing.T) {
	for _, value := range []uint64{1 << 63, 1<<64 - 1} {
		model := new(contractEmbeddingModel)
		var calls atomic.Int32
		store := newNativeContractStore(t, StoreConfig{EmbeddingModel: model}, func(writer http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			fmt.Fprint(writer, `{"root":{"coverage":{"coverage":100,"full":true},"children":[]}}`)
		})
		predicate := filter.EQ("value", value)
		if response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate}}); err == nil || response != nil {
			t.Errorf("integer %d accepted: response=%v error=%v", value, response, err)
		}
		if err := store.DeleteWhere(t.Context(), predicate); err == nil {
			t.Errorf("integer %d accepted for deletion", value)
		}
		if calls.Load() != 0 || model.calls.Load() != 0 {
			t.Fatalf("invalid request reached I/O: HTTP=%d embedding=%d", calls.Load(), model.calls.Load())
		}
	}
}

func TestSearchReadsCanonicalMetadataInsteadOfNativeDefaults(t *testing.T) {
	for _, source := range []map[string]any{nil, {"active": nil}, {"active": false}, {"value": ""}, {"value": []any{}}, {"profile": map[string]any{"active": false, "value": "", "missing": nil}}} {
		t.Run(fmt.Sprint(source), func(t *testing.T) {
			var stored atomic.Value
			store := newNativeContractStore(t, StoreConfig{}, func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/search/" {
					var body struct {
						Fields metadata.Map `json:"fields"`
					}
					if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
						t.Error(err)
					}
					stored.Store(body.Fields)
					fmt.Fprint(writer, `{}`)
					return
				}
				fields := metadata.Map{"content": []byte(`"text"`), "active": []byte(`true`), "value": []byte(`"native default"`)}
				if raw, exists := stored.Load().(metadata.Map)["scope_metadata"]; exists {
					fields["scope_metadata"] = raw
				}
				if err := jsonv2.MarshalWrite(writer, map[string]any{"root": map[string]any{"coverage": map[string]any{"coverage": 100, "full": true}, "children": []map[string]any{{"id": "id:scope:document::one", "relevance": 0.8, "fields": fields}}}}); err != nil {
					t.Error(err)
				}
			})
			attributes, err := metadata.FromValues(source)
			if err != nil {
				t.Fatal(err)
			}
			if indexErr := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "text", Metadata: attributes}}}); indexErr != nil {
				t.Fatal(indexErr)
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "text"})
			if err != nil || len(response.Results) != 1 {
				t.Fatalf("response=%v error=%v", response, err)
			}
			got := response.Results[0].Document.Metadata
			if !maps.EqualFunc(got, attributes, func(left, right json.RawMessage) bool { return string(left) == string(right) }) {
				t.Fatalf("metadata=%v want original %v", got, attributes)
			}
		})
	}
}

func TestMetadataPresenceOwnsAtomicFilterTruth(t *testing.T) {
	for _, source := range []string{`active == false`, `value like '%'`, `n < 1`, `value is null`, `value is not null`, `not (active == false)`} {
		predicate, err := filter.Parse(source)
		if err != nil {
			t.Fatal(err)
		}
		compiled, err := (&Store{}).buildFilter(predicate)
		if err != nil || !strings.Contains(compiled, "scope_metadata_paths contains ") {
			t.Errorf("filter %s has no presence projection: %q error=%v", source, compiled, err)
		}
	}
}

func TestIndexReplacesMetadataAndItsPresenceTogether(t *testing.T) {
	var fields atomic.Value
	store := newNativeContractStore(t, StoreConfig{}, func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Fields metadata.Map `json:"fields"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
		}
		fields.Store(body.Fields)
		fmt.Fprint(writer, `{}`)
	})
	attributes, err := metadata.FromValues(map[string]any{"active": false, "null_value": nil, "empty_array": []any{}, "empty_object": map[string]any{}, "profile": map[string]any{"active": false, "missing": nil}})
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []metadata.Map{attributes, nil} {
		if indexErr := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "text", Metadata: current}}}); indexErr != nil {
			t.Fatal(indexErr)
		}
		stored := fields.Load().(metadata.Map)
		encoded, present, decodeErr := stored.Decode[string]("scope_metadata")
		if decodeErr != nil || !present {
			t.Fatalf("metadata object: present=%t error=%v", present, decodeErr)
		}
		var decoded metadata.Map
		if decodeErr = jsonv2.Unmarshal([]byte(encoded), &decoded); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if !maps.EqualFunc(decoded, current, func(left, right json.RawMessage) bool { return string(left) == string(right) }) {
			t.Fatalf("stored metadata=%v want %v", decoded, current)
		}
		paths, present, decodeErr := stored.Decode[[]string]("scope_metadata_paths")
		if decodeErr != nil || !present {
			t.Fatalf("presence paths: present=%t error=%v", present, decodeErr)
		}
		want := []string{}
		if current != nil {
			want = []string{`["active"]`, `["empty_array"]`, `["empty_object"]`, `["profile","active"]`, `["profile"]`}
		}
		if !slices.Equal(paths, want) {
			t.Fatalf("presence paths=%v want %v", paths, want)
		}
		if current == nil {
			if _, exists := stored["active"]; exists {
				t.Fatal("obsolete filtering attribute survived replacement")
			}
		}
	}
}

func TestCurrentMetadataRequiredBySearchAndDeletion(t *testing.T) {
	for _, raw := range []string{"", `null`, `"{broken"`, `"null"`, `"[]"`, `"{\"scope_namespace\":\"foreign\"}"`} {
		t.Run(raw, func(t *testing.T) {
			var deletes atomic.Int32
			store := newNativeContractStore(t, StoreConfig{}, func(writer http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodDelete {
					deletes.Add(1)
					fmt.Fprint(writer, `{}`)
					return
				}
				fields := metadata.Map{"content": []byte(`"text"`)}
				if raw != "" {
					fields["scope_metadata"] = []byte(raw)
				}
				children := []map[string]any{
					{"id": "id:scope:document::valid", "relevance": 0.8, "fields": map[string]any{"content": "text", "scope_metadata": `{"safe":true}`}},
					{"id": "id:scope:document::invalid", "relevance": 0.8, "fields": fields},
				}
				if err := jsonv2.MarshalWrite(writer, map[string]any{"root": map[string]any{"coverage": map[string]any{"coverage": 100, "full": true}, "children": children}}); err != nil {
					t.Error(err)
				}
			})
			if response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "text"}); err == nil || response != nil {
				t.Errorf("invalid metadata accepted: response=%v error=%v", response, err)
			}
			if err := store.DeleteWhere(t.Context(), filter.EQ("safe", true)); err == nil {
				t.Error("invalid metadata accepted for deletion")
			}
			if deletes.Load() != 0 {
				t.Fatalf("deleted %d documents before the page proved its current schema", deletes.Load())
			}
		})
	}
}

func TestPresencePathsPreserveKeySegments(t *testing.T) {
	attributes, err := metadata.FromValues(map[string]any{"profile.child": false, "profile": map[string]any{"child": false, "literal.name": "", "empty": map[string]any{}, "null": nil}})
	if err != nil {
		t.Fatal(err)
	}
	_, paths, err := projectMetadata(attributes)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`["profile","child"]`, `["profile","empty"]`, `["profile","literal.name"]`, `["profile"]`, `["profile.child"]`}
	if !slices.Equal(paths, want) {
		t.Fatalf("presence paths=%v want %v", paths, want)
	}
}
