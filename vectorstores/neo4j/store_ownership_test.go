package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	native "github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestDocumentPropertiesPreserveCoreMetadata(t *testing.T) {
	store := &Store{idProperty: "id", textProperty: "text", metadataProperty: "metadata", embeddingProperty: "embedding"}
	cases := []metadata.Map{nil, {}, {"nested": json.RawMessage(`{"value":[null,1e1000,"\u0061"]}`), "decimal": json.RawMessage(`1.00000000000000001`), "exponent": json.RawMessage(`9007199254740993.0`), "null": json.RawMessage(`null`)}}
	for _, values := range cases {
		properties, err := store.documentProperties(&document.Document{ID: "id", Text: "hello", Metadata: values}, []float64{1, 0})
		if err != nil {
			t.Fatal(err)
		}
		if len(properties) != 4 || properties["id"] != "id" || properties["text"] != "hello" {
			t.Fatalf("properties=%v", properties)
		}
		var decoded metadata.Map
		if err := decoded.UnmarshalJSON([]byte(properties["metadata"].(string))); err != nil {
			t.Fatal(err)
		}
		want := values.Clone()
		if want != nil && want["nested"] != nil {
			want["nested"] = json.RawMessage(`{"value":[null,1e1000,"a"]}`)
		}
		if !want.Equal(decoded) {
			t.Fatalf("metadata=%s want %s", decoded, want)
		}
		if (values == nil) != (decoded == nil) {
			t.Fatalf("nil metadata collapsed: %v", properties)
		}
	}
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	valid := StoreConfig{Driver: &testDriver{}, EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) { return nil, nil }), DocumentBatcher: testBatcher{}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.applyDefaults()
	for _, name := range []string{"Label", "IDProperty", "TextProperty", "EmbeddingProperty", "MetadataProperty"} {
		config := valid
		reflect.ValueOf(&config).Elem().FieldByName(name).SetString("bad`name")
		if err := config.Validate(); err == nil {
			t.Fatalf("accepted invalid %s", name)
		}
	}
	for _, name := range []string{"TextProperty", "EmbeddingProperty", "MetadataProperty"} {
		config := valid
		reflect.ValueOf(&config).Elem().FieldByName(name).SetString(valid.IDProperty)
		if err := config.Validate(); err == nil {
			t.Fatalf("accepted colliding %s", name)
		}
	}
	invalid := []StoreConfig{valid, valid, valid, valid}
	invalid[0].Driver = nil
	invalid[1].EmbeddingModel = nil
	invalid[2].DocumentBatcher = nil
	invalid[3].Similarity = "wrong"
	for _, config := range invalid {
		if err := config.Validate(); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
	if SimilarityCosine.String() != "cosine" || !SimilarityEuclidean.Valid() {
		t.Fatal("invalid similarity contract")
	}
	if got := quoteIdentifier("a`b"); got != "`a``b`" {
		t.Fatal(got)
	}
}

func recordWith(properties map[string]any) *native.Record {
	return &native.Record{Keys: []string{"properties", "elementID", "selfScore"}, Values: []any{properties, "element-id", float64(1)}}
}

func TestDecodeRejectsMalformedCurrentNode(t *testing.T) {
	store := &Store{idProperty: "id", textProperty: "text", metadataProperty: "metadata", embeddingProperty: "embedding"}
	valid := func() map[string]any {
		return map[string]any{"id": "original", "text": "hello", "metadata": `{"value":1e1000}`, "embedding": []any{float64(1), float64(0)}}
	}
	node, err := store.decodeRecord(recordWith(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if node.document.ID != "original" || string(node.document.Metadata["value"]) != "1e1000" || !reflect.DeepEqual(node.vector, []float64{1, 0}) {
		t.Fatalf("node=%+v", node)
	}
	cases := []func(map[string]any){
		func(p map[string]any) { p["foreign"] = true }, func(p map[string]any) { delete(p, "metadata") },
		func(p map[string]any) { p["id"] = " " }, func(p map[string]any) { p["text"] = "" }, func(p map[string]any) { p["text"] = 7 },
		func(p map[string]any) { p["metadata"] = map[string]any{} }, func(p map[string]any) { p["metadata"] = `{"a":1,"a":2}` }, func(p map[string]any) { p["metadata"] = `[]` },
		func(p map[string]any) { p["embedding"] = []any{} }, func(p map[string]any) { p["embedding"] = []any{nil} }, func(p map[string]any) { p["embedding"] = []any{int64(1)} }, func(p map[string]any) { p["embedding"] = []any{math.Inf(1)} },
	}
	for i, change := range cases {
		properties := valid()
		change(properties)
		if _, err := store.decodeRecord(recordWith(properties)); err == nil {
			t.Fatalf("accepted malformed case %d", i)
		}
	}
	for _, values := range [][]any{{nil, "element-id", float64(1)}, {valid(), nil, float64(1)}, {valid(), "element-id", nil}, {valid(), "element-id", float64(2)}} {
		record := &native.Record{Keys: []string{"properties", "elementID", "selfScore"}, Values: values}
		if _, err := store.decodeRecord(record); err == nil {
			t.Fatalf("accepted malformed record %v", values)
		}
	}
}

type testBatcher struct{}

func (testBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

type testDriver struct {
	session *testSession
	config  native.SessionConfig
}

func (t *testDriver) NewSession(_ context.Context, config native.SessionConfig) native.SessionWithContext {
	t.config = config
	return t.session
}

type testSession struct {
	native.SessionWithContext
	closeErr error
	workErr  error
	closed   bool
}

func (t *testSession) ExecuteRead(ctx context.Context, work native.ManagedTransactionWork, _ ...func(*native.TransactionConfig)) (any, error) {
	if t.workErr != nil {
		return nil, t.workErr
	}
	return work(&testTransaction{})
}
func (t *testSession) ExecuteWrite(ctx context.Context, work native.ManagedTransactionWork, _ ...func(*native.TransactionConfig)) (any, error) {
	return t.ExecuteRead(ctx, work)
}
func (t *testSession) Close(ctx context.Context) error {
	t.closed = ctx.Err() == nil
	return t.closeErr
}

type testTransaction struct{ native.ManagedTransaction }

func TestTransactionReturnsCloseAndCancellationErrors(t *testing.T) {
	closeFailure, workFailure := errors.New("close failure"), errors.New("work failure")
	for _, mode := range []native.AccessMode{native.AccessModeRead, native.AccessModeWrite} {
		session := &testSession{closeErr: closeFailure, workErr: workFailure}
		driver := &testDriver{session: session}
		store := &Store{driver: driver, database: "isolated"}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		value, err := store.transact(ctx, mode, func(native.ManagedTransaction) (any, error) { t.Fatal("work called"); return nil, nil })
		if value != nil || !errors.Is(err, closeFailure) || !errors.Is(err, workFailure) || !errors.Is(err, context.Canceled) || !session.closed {
			t.Fatalf("value=%v err=%v closed=%v", value, err, session.closed)
		}
		if driver.config.DatabaseName != "isolated" || driver.config.AccessMode != mode {
			t.Fatal(driver.config)
		}
	}
	session := &testSession{}
	store := &Store{driver: &testDriver{session: session}}
	value, err := store.transact(t.Context(), native.AccessModeRead, func(native.ManagedTransaction) (any, error) { return "result", nil })
	if err != nil || value != "result" || !session.closed {
		t.Fatalf("value=%v err=%v", value, err)
	}
}

func TestInvalidRequestsFailBeforeModelOrDriver(t *testing.T) {
	// Invalid requests and unsupported modes are also exercised by storetest.Run.
	store := new(Store)
	if err := store.Index(t.Context(), nil); err == nil {
		t.Fatal("accepted nil")
	}
	response, searchErr := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid}})
	if response != nil || !errors.Is(searchErr, vectorstore.ErrUnsupportedSearchMode) {
		t.Fatalf("response=%v err=%v", response, searchErr)
	}
	if err := store.DeleteWhere(t.Context(), nil); !errors.Is(err, vectorstore.ErrMissingFilter) {
		t.Fatal(err)
	}
	if err := store.DeleteIDs(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	_, err := store.documentProperties(&document.Document{ID: "x", Metadata: metadata.Map{"value": json.RawMessage(`invalid`)}}, nil)
	if err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatal(err)
	}
}
