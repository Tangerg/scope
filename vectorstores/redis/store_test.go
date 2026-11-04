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
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

const fixtureSpace vectorstore.SpaceID = "paired-v1"

var fixturePrefix = "embedding:" + fixtureSpace.StorageKey() + ":"

func fixtureVector(text string) []float64 {
	if text == "far" {
		return []float64{0, 1}
	}
	return []float64{1, 0}
}
func fixtureBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: fixtureSpace}
	for _, doc := range docs {
		b.Records = append(b.Records, vectorstore.Record{Document: doc, Values: fixtureVector(doc.Text)})
	}
	return b
}
func fixtureBudget() vectorstore.ScanBudget {
	return vectorstore.ScanBudget{MaxRecords: 10000, MaxBytes: 64 << 20, Timeout: time.Minute}
}
func fixtureQuery(ctx context.Context, store *Store, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	if q.Options.Filter != nil {
		return store.QueryByScan(ctx, q, fixtureBudget())
	}
	return store.Query(ctx, q)
}

type protocolFixture struct {
	mu                         sync.Mutex
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
	scans, payloads            int
	scanErr                    error
	uncertainKey               string
}

func newProtocolFixture(t *testing.T) *protocolFixture {
	t.Helper()
	fixture := &protocolFixture{t: t, rows: make(map[string]map[string]string), distances: make(map[string]string), info: map[string]any{"index_name": "documents", "index_options": []any{}, "index_definition": map[string]any{"key_type": "HASH", "prefixes": []any{fixturePrefix}}, "attributes": []any{map[string]any{"identifier": embeddingField, "attribute": embeddingField, "type": "VECTOR", "data_type": "FLOAT32", "distance_metric": "COSINE", "dim": int64(2)}, map[string]any{"identifier": termsField, "attribute": termsField, "type": "TAG", "SEPARATOR": ","}}}}
	// The SDK classifies hostnames with DNS before installing command hooks.
	// A loopback literal keeps this protocol fixture independent of any resolver.
	fixture.client = goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:0"})
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
	p.mu.Lock()
	defer p.mu.Unlock()
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
		script := args[1].(string)
		if script == scanKeyPage {
			p.scans++
			if p.scanErr != nil {
				return p.scanErr
			}
			keys := []any{}
			n := 0
			for _, k := range slices.Sorted(maps.Keys(p.rows)) {
				keys = append(keys, k)
				n += len(k)
			}
			if len(keys) > args[6].(int) || int64(n) > args[7].(int64) {
				command.(*goredis.Cmd).SetVal([]any{int64(-1)})
			} else {
				command.(*goredis.Cmd).SetVal([]any{"0", keys})
			}
			return nil
		}
		key := args[3].(string)
		if script == readHash {
			f := p.rows[key]
			if len(f) == 0 {
				command.(*goredis.Cmd).SetVal(int64(0))
				return nil
			}
			if len(f) != 4 {
				command.(*goredis.Cmd).SetVal(int64(-1))
				return nil
			}
			n := int64(len(key))
			values := []any{}
			for _, k := range args[5:] {
				k := k.(string)
				n += int64(len(k) + len(f[k]))
				v, ok := f[k]
				if !ok {
					values = append(values, nil)
				} else {
					values = append(values, v)
				}
			}
			switch args[4] {
			case "measure":
				command.(*goredis.Cmd).SetVal(n)
			case n:
				p.payloads++
				command.(*goredis.Cmd).SetVal(values)
			default:
				command.(*goredis.Cmd).SetVal(int64(-1))
			}
			return nil
		}
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
			if p.uncertainKey == key {
				value = 0
			}
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
			if base, ok := strings.CutPrefix(args[2].(string), "(@"+termsField+":{"); ok {
				term, _, _ := strings.Cut(base, "})")
				if !slices.Contains(strings.Split(fields[termsField], ","), term) {
					continue
				}
			}
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
func (p *protocolFixture) scanNodes(ctx context.Context, visit func(context.Context, *goredis.Client) error) error {
	return visit(ctx, p.client)
}

func (p *protocolFixture) store(size int) *Store {
	p.t.Helper()
	store, err := NewStore(p.t.Context(), StoreConfig{Client: p.client, ScanNodes: p.scanNodes, IndexName: "documents", Space: fixtureSpace, WriteBatchSize: size})
	if err != nil {
		p.t.Fatal(err)
	}
	return store
}

func TestCoreFilterConformanceThroughNativeKeySelection(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store(32)
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		clear(fixture.rows)
		if err := store.Upsert(ctx, fixtureBatch(docs)); err != nil {
			return nil, err
		}
		response, err := fixtureQuery(ctx, store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
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
	store := fixture.store(32)
	for _, facts := range []metadata.Map{nil, {}, {"content": json.RawMessage(`"user content"`), "embedding": json.RawMessage(`{"nested":[1,null]}`), "metadata_json": json.RawMessage(`9007199254740993`), "decimal": json.RawMessage(`1.0000000000000000001`), "huge": json.RawMessage(`1e1000`)}} {
		for _, id := range []string{"one", " spaced ", "*?[]\\", "中文🙂"} {
			clear(fixture.rows)
			doc := &document.Document{ID: id, Text: "text", Metadata: facts}
			if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{doc})); err != nil {
				t.Fatal(err)
			}
			if len(fixture.rows[fixturePrefix+id]) != 4 {
				t.Fatal("metadata created shadow HASH fields")
			}
			response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{Filter: filter.IsNull("unused")}})
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
	for _, fault := range []string{"dimension", "overflow", "underflow", "metadata"} {
		t.Run(fault, func(t *testing.T) {
			f := newProtocolFixture(t)
			s := f.store(1)
			b := fixtureBatch([]*document.Document{{ID: "one", Text: "text"}, {ID: "two", Text: "text"}})
			switch fault {
			case "dimension":
				b.Records[1].Values = []float64{1}
			case "overflow":
				b.Records[1].Values = []float64{1e100, 0}
			case "underflow":
				b.Records[1].Values = []float64{1e-100, 0}
			case "metadata":
				b.Records[1].Document.Metadata = metadata.Map{"bad": json.RawMessage("invalid")}
			}
			if err := s.Upsert(t.Context(), b); err == nil || f.writes != 0 {
				t.Fatalf("err=%v writes=%d", err, f.writes)
			}
		})
	}

}

func TestNativeSchemaOwnsMetricAndNamespace(t *testing.T) {
	for _, metric := range []nativeMetric{metricCosine, metricL2, metricIP} {
		t.Run(string(metric), func(t *testing.T) {
			fixture := newProtocolFixture(t)
			fields := fixture.info["attributes"].([]any)[0].(map[string]any)
			fields["distance_metric"] = string(metric)
			store := fixture.store(32)
			if store.schema.metric != metric || store.schema.dimensions != 2 || store.schema.prefix != fixturePrefix {
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
				fixture.info["index_definition"] = map[string]any{"key_type": "JSON", "prefixes": []any{fixturePrefix}}
			case "filter":
				fixture.info["index_definition"] = map[string]any{"key_type": "HASH", "prefixes": []any{fixturePrefix}, "filter": "@value=='x'"}
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
			store, err := NewStore(t.Context(), StoreConfig{Client: fixture.client, ScanNodes: fixture.scanNodes, IndexName: "documents", Space: fixtureSpace})
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
			store := fixture.store(32)
			if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text"}})); err != nil {
				t.Fatal(err)
			}
			fields := fixture.rows[fixturePrefix+"one"]
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
				delete(fixture.rows, fixturePrefix+"one")
				fixture.rows[fixturePrefix+" "] = fields
			}
			response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{Filter: filter.IsNull("absent")}})
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
	store := fixture.store(32)
	facts, err := metadata.FromValues(map[string]any{"value": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text", Metadata: facts}})); err != nil {
		t.Fatal(err)
	}
	fixture.beforeDelete = func() {
		facts := metadata.Map{"value": json.RawMessage(`"y"`)}
		tags, tagErr := tagValues(facts)
		if tagErr != nil {
			t.Fatal(tagErr)
		}
		fixture.rows[fixturePrefix+"one"][metadataField] = `{"value":"y"}`
		fixture.rows[fixturePrefix+"one"][termsField] = tags
	}
	if err = store.DeleteMatching(t.Context(), filter.EQ("value", "x"), fixtureBudget()); err == nil || fixture.deleted != 0 {
		t.Fatalf("stale deletion=%v, deleted=%d", err, fixture.deleted)
	}
	if err = store.DeleteMatching(t.Context(), filter.EQ("value", "y"), fixtureBudget()); err != nil || fixture.deleted != 1 {
		t.Fatalf("current deletion=%v, deleted=%d", err, fixture.deleted)
	}
}

func TestFilteredGroupsPreserveRawDistanceRanking(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.info["attributes"].([]any)[0].(map[string]any)["distance_metric"] = "IP"
	store := fixture.store(32)
	docs := make([]*document.Document, 257)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("%04d", i), Text: "text"}
	}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	for i, doc := range docs {
		fixture.distances[fixturePrefix+doc.ID] = strconv.FormatFloat(-1e30-float64(i)*1e29, 'g', -1, 64)
	}
	response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.IsNull("unused")}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "0256" {
		t.Fatalf("raw native rank changed: %#v, %v", response, err)
	}
	if !slices.Equal(fixture.queries, []int{256, 1}) {
		t.Fatalf("native groups=%v", fixture.queries)
	}
}

func TestNativeTagQueryNeedsNoScanGrant(t *testing.T) {
	f := newProtocolFixture(t)
	s := f.store(2)
	f.scanErr = errors.New("scan forbidden")
	m := func(v string) metadata.Map { return metadata.Map{"tag": json.RawMessage(v)} }
	b := fixtureBatch([]*document.Document{{ID: "scalar", Text: "text", Metadata: m(`"X|{x},"`)}, {ID: "array", Text: "text", Metadata: m(`["X|{x},"]`)}, {ID: "case", Text: "text", Metadata: m(`"x|{x},"`)}})
	if err := s.Upsert(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	q := &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Filter: filter.EQ("tag", "X|{x},")}}
	r, err := s.Query(t.Context(), q)
	if err != nil || len(r.Results) != 1 || r.Results[0].Document.ID != "scalar" || f.scans != 0 {
		t.Fatalf("native result=%v err=%v scans=%d", r, err, f.scans)
	}
	q.Options.Filter = filter.EQ("tag", 2)
	if r, err = s.Query(t.Context(), q); !errors.Is(err, vectorstore.ErrUnsupportedFilter) || r != nil || f.scans != 0 {
		t.Fatalf("unsupported=%v err=%v scans=%d", r, err, f.scans)
	}
}
func TestPipelineEvidenceSeparatesSentAndUnsent(t *testing.T) {
	f := newProtocolFixture(t)
	s := f.store(2)
	f.uncertainKey = fixturePrefix + "b"
	b := fixtureBatch([]*document.Document{{ID: "a", Text: "text"}, {ID: "b", Text: "text"}, {ID: "c", Text: "text"}})
	err := s.Upsert(t.Context(), b)
	var w *vectorstore.WriteError
	if !errors.As(err, &w) || !slices.Equal(w.Acknowledged, []string{"a"}) || !slices.Equal(w.Unconfirmed, []string{"b"}) || !slices.Equal(w.Unattempted, []string{"c"}) || f.writes != 2 {
		t.Fatalf("evidence=%+v writes=%d", w, f.writes)
	}
}
func TestGrantDebitedBeforeTransferringHashPayload(t *testing.T) {
	f := newProtocolFixture(t)
	s := f.store(1)
	if err := s.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: strings.Repeat("x", 1024)}})); err != nil {
		t.Fatal(err)
	}
	q := &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Filter: filter.IsNull("absent")}}
	b := vectorstore.ScanBudget{MaxRecords: 1, MaxBytes: 256, Timeout: time.Second}
	if r, err := s.QueryByScan(t.Context(), q, b); !errors.Is(err, vectorstore.ErrScanBudget) || r != nil || f.payloads != 0 {
		t.Fatalf("got=%v err=%v transferred=%d", r, err, f.payloads)
	}
	if err := s.DeleteMatching(t.Context(), q.Options.Filter, b); !errors.Is(err, vectorstore.ErrScanBudget) || f.deleted != 0 {
		t.Fatalf("deleted=%d err=%v", f.deleted, err)
	}
}
func TestSameWidthSpaceRebindingPreventsMutation(t *testing.T) {
	f := newProtocolFixture(t)
	s := f.store(1)
	f.info["index_definition"].(map[string]any)["prefixes"] = []any{vectorstore.SpaceID("different").StorageKey() + ":"}
	if err := s.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "x"}})); !errors.Is(err, vectorstore.ErrIncompatibleSpace) || f.writes != 0 {
		t.Fatalf("writes=%d err=%v", f.writes, err)
	}
}
