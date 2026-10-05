package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type ownershipBatcher struct{ size int }

func (o ownershipBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return slices.Collect(slices.Chunk(docs, max(o.size, 1))), nil
}
func constantModel() embedding.Model {
	return embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
}

type protocolFixture struct {
	t                          *testing.T
	client                     *goredis.Client
	info                       map[string]any
	rows                       map[string]map[string]string
	distances                  map[string]string
	queries                    []int
	writes, deleted            int
	beforeDelete, beforeSearch func()
	searchReply                any
	ackOverride                *int64
}

func newProtocolFixture(t *testing.T) *protocolFixture {
	t.Helper()
	fixture := &protocolFixture{t: t, rows: make(map[string]map[string]string), distances: make(map[string]string), info: map[string]any{"index_name": "documents", "index_options": []any{}, "index_definition": map[string]any{"key_type": "HASH", "prefixes": []any{"embedding:"}}, "attributes": []any{map[string]any{"identifier": embeddingField, "attribute": embeddingField, "type": "VECTOR", "data_type": "FLOAT32", "distance_metric": "COSINE", "dim": int64(2)}}}}
	fixture.client = goredis.NewClient(&goredis.Options{Addr: "unused"})
	fixture.client.AddHook(fixture)
	t.Cleanup(func() {
		if err := fixture.client.Close(); err != nil {
			t.Error(err)
		}
	})
	return fixture
}
func (p *protocolFixture) DialHook(next goredis.DialHook) goredis.DialHook       { return next }
func (p *protocolFixture) ProcessHook(_ goredis.ProcessHook) goredis.ProcessHook { return p.process }
func (p *protocolFixture) ProcessPipelineHook(_ goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return func(ctx context.Context, commands []goredis.Cmder) error {
		for _, command := range commands {
			if err := p.process(ctx, command); err != nil {
				command.SetErr(err)
				return err
			}
		}
		return nil
	}
}
func (p *protocolFixture) process(ctx context.Context, command goredis.Cmder) error {
	if err := ctx.Err(); err != nil {
		command.SetErr(err)
		return err
	}
	args := command.Args()
	switch command.Name() {
	case "ft.info":
		command.(*goredis.Cmd).SetVal(p.info)
	case "scan":
		command.(*goredis.ScanCmd).SetVal(slices.Sorted(maps.Keys(p.rows)), 0)
	case "hgetall":
		command.(*goredis.MapStringStringCmd).SetVal(maps.Clone(p.rows[args[1].(string)]))
	case "eval":
		script, key := args[1].(string), args[3].(string)
		switch script {
		case replaceDocumentHash:
			fields := make(map[string]string)
			for i := 4; i < len(args); i += 2 {
				var value string
				switch v := args[i+1].(type) {
				case string:
					value = v
				case []byte:
					value = string(v)
				default:
					return fmt.Errorf("unexpected native HASH value %T", v)
				}
				fields[args[i].(string)] = value
			}
			p.rows[key] = fields
			p.writes++
			value := int64(1)
			if p.ackOverride != nil {
				value = *p.ackOverride
			}
			command.(*goredis.Cmd).SetVal(value)
		case deleteObservedMetadata:
			if p.beforeDelete != nil {
				p.beforeDelete()
				p.beforeDelete = nil
			}
			value := int64(0)
			if p.rows[key][args[4].(string)] == args[5].(string) {
				delete(p.rows, key)
				p.deleted++
				value = 1
			}
			command.(*goredis.Cmd).SetVal(value)
		default:
			return errors.New("unexpected native script")
		}
	case "del":
		if len(args) != 2 {
			return errors.New("explicit DEL crossed a native key boundary")
		}
		value := int64(0)
		if _, ok := p.rows[args[1].(string)]; ok {
			delete(p.rows, args[1].(string))
			p.deleted++
			value = 1
		}
		command.(*goredis.IntCmd).SetVal(value)
	case "ft.search":
		if p.beforeSearch != nil {
			p.beforeSearch()
			p.beforeSearch = nil
		}
		if p.searchReply != nil {
			command.(*goredis.Cmd).SetVal(p.searchReply)
			return nil
		}
		var selected []string
		keysRestricted := false
		limit := 0
		for i, arg := range args {
			if strings.EqualFold(fmt.Sprint(arg), "INKEYS") {
				keysRestricted = true
				count := args[i+1].(int)
				for _, key := range args[i+2 : i+2+count] {
					selected = append(selected, key.(string))
				}
			}
			if strings.EqualFold(fmt.Sprint(arg), "LIMIT") {
				limit = args[i+2].(int)
			}
		}
		p.queries = append(p.queries, len(selected))
		var hits []goredis.Document
		for key, fields := range p.rows {
			if keysRestricted && !slices.Contains(selected, key) {
				continue
			}
			copy := maps.Clone(fields)
			distance := p.distances[key]
			if distance == "" {
				distance = "0"
			}
			copy[distanceFieldName] = distance
			hits = append(hits, goredis.Document{ID: key, Fields: copy})
		}
		slices.SortFunc(hits, func(left, right goredis.Document) int {
			a, _ := strconv.ParseFloat(left.Fields[distanceFieldName], 64)
			b, _ := strconv.ParseFloat(right.Fields[distanceFieldName], 64)
			if a < b {
				return -1
			}
			if a > b {
				return 1
			}
			return strings.Compare(left.ID, right.ID)
		})
		hits = hits[:min(len(hits), limit)]
		var results []any
		for _, hit := range hits {
			fields := make(map[string]any, len(hit.Fields))
			for key, value := range hit.Fields {
				fields[key] = value
			}
			results = append(results, map[string]any{"id": hit.ID, "extra_attributes": fields})
		}
		command.(*goredis.Cmd).SetVal(map[string]any{"format": "STRING", "warning": []any{}, "total_results": int64(len(hits)), "results": results})
	default:
		return fmt.Errorf("unexpected native command: %s", command.Name())
	}
	return nil
}
func (p *protocolFixture) store(model embedding.Model, size int) *Store {
	p.t.Helper()
	store, err := NewStore(p.t.Context(), StoreConfig{Client: p.client, IndexName: "documents", EmbeddingModel: model, DocumentBatcher: ownershipBatcher{size: size}})
	if err != nil {
		p.t.Fatal(err)
	}
	return store
}

func TestCoreFilterConformanceThroughNativeKeySelection(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store(constantModel(), 32)
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		clear(fixture.rows)
		if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: docs}); err != nil {
			return nil, err
		}
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, hit := range response.Results {
			ids = append(ids, hit.Document.ID)
		}
		return ids, nil
	}})
}

func TestCoreMetadataHasOnlyOneHASHRepresentation(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store(constantModel(), 32)
	for _, facts := range []metadata.Map{nil, {}, {"content": json.RawMessage(`"user content"`), "embedding": json.RawMessage(`{"nested":[1,null]}`), "metadata_json": json.RawMessage(`9007199254740993`), "decimal": json.RawMessage(`1.0000000000000000001`), "huge": json.RawMessage(`1e1000`)}} {
		for _, id := range []string{"one", " spaced ", "*?[]\\", "中文🙂"} {
			clear(fixture.rows)
			doc := &document.Document{ID: id, Text: "text", Metadata: facts}
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{doc}}); err != nil {
				t.Fatal(err)
			}
			if len(fixture.rows["embedding:"+id]) != 3 {
				t.Fatal("metadata created shadow HASH fields")
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: filter.IsNull("unused")}})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Document.ID != id || !facts.Equal(response.Results[0].Document.Metadata) || (facts == nil) != (response.Results[0].Document.Metadata == nil) {
				t.Fatalf("roundtrip changed facts: %#v", response)
			}
			if err = store.DeleteIDs(t.Context(), []string{id, id, "unknown"}); err != nil {
				t.Fatal(err)
			}
			if len(fixture.rows) != 0 {
				t.Fatal("explicit deletion missed native keys")
			}
		}
	}
}

func TestWholeIndexPreparationBeforePublication(t *testing.T) {
	for _, fault := range []string{"late model", "dimension", "overflow", "underflow", "metadata"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			calls := 0
			cause := errors.New("late model failure")
			model := embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
				calls++
				if calls == 2 && fault == "late model" {
					return nil, cause
				}
				vector := []float64{1, 0}
				if calls == 2 {
					switch fault {
					case "dimension":
						vector = []float64{1}
					case "overflow":
						vector = []float64{1e100, 0}
					case "underflow":
						vector = []float64{1e-100, 0}
					}
				}
				return embedding.NewResponse([]*embedding.Output{{Embedding: vector}}, nil)
			})
			store := fixture.store(model, 1)
			docs := []*document.Document{{ID: "one", Text: "text"}, {ID: "two", Text: "text"}}
			if fault == "metadata" {
				docs[1].Metadata = metadata.Map{"bad": json.RawMessage(`broken`)}
			}
			err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs})
			if err == nil || fixture.writes != 0 {
				t.Fatalf("error=%v, published=%d", err, fixture.writes)
			}
			if fault == "late model" && !errors.Is(err, cause) {
				t.Fatal("lost model cause")
			}
			if fault == "metadata" && calls != 0 {
				t.Fatal("bad metadata reached model")
			}
		})
	}
}

func TestNativeSchemaOwnsMetricAndNamespace(t *testing.T) {
	for _, metric := range []string{"COSINE", "L2", "IP"} {
		t.Run(metric, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			fields := fixture.info["attributes"].([]any)[0].(map[string]any)
			fields["distance_metric"] = metric
			store := fixture.store(constantModel(), 32)
			if store.schema.metric != metric || store.schema.dimensions != 2 || store.schema.prefix != "embedding:" {
				t.Fatalf("schema=%#v", store.schema)
			}
		})
	}
	for _, fault := range []string{"alias", "all keys", "JSON", "filter", "extra field", "aliased vector", "float64", "bad metric", "missing dimension"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			attributes := fixture.info["attributes"].([]any)
			field := attributes[0].(map[string]any)
			switch fault {
			case "alias":
				fixture.info["index_name"] = "other"
			case "all keys":
				fixture.info["index_definition"] = map[string]any{"key_type": "HASH", "prefixes": []any{""}}
			case "JSON":
				fixture.info["index_definition"] = map[string]any{"key_type": "JSON", "prefixes": []any{"embedding:"}}
			case "filter":
				fixture.info["index_definition"] = map[string]any{"key_type": "HASH", "prefixes": []any{"embedding:"}, "filter": "@value=='x'"}
			case "extra field":
				fixture.info["attributes"] = append(attributes, map[string]any{"identifier": "value", "attribute": "value", "type": "TAG"})
			case "aliased vector":
				field["identifier"] = "other"
			case "float64":
				field["data_type"] = "FLOAT64"
			case "bad metric":
				field["distance_metric"] = "UNKNOWN"
			case "missing dimension":
				field["dim"] = 0
			}
			store, err := NewStore(t.Context(), StoreConfig{Client: fixture.client, IndexName: "documents", EmbeddingModel: constantModel(), DocumentBatcher: ownershipBatcher{}})
			if store != nil || !errors.Is(err, ErrIncompatibleIndex) {
				t.Fatalf("store=%#v, error=%v", store, err)
			}
		})
	}
}

func TestReadRejectsMissingFactsAndInvalidNativeDistances(t *testing.T) {
	for _, fault := range []string{"metadata", "embedding", "unknown", "bad metadata", "short vector", "zero vector", "whitespace ID"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			store := fixture.store(constantModel(), 32)
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "text"}}}); err != nil {
				t.Fatal(err)
			}
			fields := fixture.rows["embedding:one"]
			switch fault {
			case "metadata":
				delete(fields, metadataField)
			case "embedding":
				delete(fields, embeddingField)
			case "unknown":
				fields["shadow"] = "value"
			case "bad metadata":
				fields[metadataField] = "broken"
			case "short vector":
				fields[embeddingField] = "bad"
			case "zero vector":
				fields[embeddingField] = string(float32sToBytes([]float32{0, 0}))
			case "whitespace ID":
				delete(fixture.rows, "embedding:one")
				fixture.rows["embedding: "] = fields
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
			if err == nil || response != nil || len(fixture.queries) != 0 {
				t.Fatalf("corrupt record succeeded: %#v, %v", response, err)
			}
		})
	}
	for _, distance := range []float64{-1, 3, math.NaN(), math.Inf(1)} {
		if _, err := (nativeSchema{metric: "COSINE"}).score(distance); err == nil {
			t.Fatalf("invalid cosine distance %g became success", distance)
		}
	}
}

func TestConditionalDeleteReportsChangedMetadata(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store(constantModel(), 32)
	facts, err := metadata.FromValues(map[string]any{"value": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "text", Metadata: facts}}}); err != nil {
		t.Fatal(err)
	}
	fixture.beforeDelete = func() { fixture.rows["embedding:one"][metadataField] = `{"value":"y"}` }
	if err = store.DeleteWhere(t.Context(), filter.EQ("value", "x")); err == nil || fixture.deleted != 0 {
		t.Fatalf("stale deletion=%v, deleted=%d", err, fixture.deleted)
	}
	if err = store.DeleteWhere(t.Context(), filter.EQ("value", "y")); err != nil || fixture.deleted != 1 {
		t.Fatalf("current deletion=%v, deleted=%d", err, fixture.deleted)
	}
}

func TestFilteredGroupsPreserveRawDistanceRanking(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.info["attributes"].([]any)[0].(map[string]any)["distance_metric"] = "IP"
	store := fixture.store(constantModel(), 32)
	docs := make([]*document.Document, 257)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("%04d", i), Text: "text"}
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	for i, doc := range docs {
		fixture.distances["embedding:"+doc.ID] = strconv.FormatFloat(-1e30-float64(i)*1e29, 'g', -1, 64)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.IsNull("unused")}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "0256" {
		t.Fatalf("raw native rank changed: %#v, %v", response, err)
	}
	if !slices.Equal(fixture.queries, []int{256, 1}) {
		t.Fatalf("native groups=%v", fixture.queries)
	}
}
