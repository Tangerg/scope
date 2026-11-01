package weaviate

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	sdk "github.com/weaviate/weaviate-go-client/v6"

	"github.com/weaviate/weaviate-go-client/v6/collections"
	"github.com/weaviate/weaviate-go-client/v6/collections/vectorindex"
	"github.com/weaviate/weaviate-go-client/v6/modules/selfprovided"
	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

// The peer serves the official HTTP and gRPC protocols.
type nativeFixture struct {
	pb.UnimplementedWeaviateServer
	apiKey                  string
	t                       *testing.T
	mu                      sync.Mutex
	class                   *collections.Collection
	wireClass               map[string]any
	objects                 map[string]*pb.BatchObject
	batches, deletes        atomic.Int64
	batchReply              func(*pb.BatchObjectsRequest) (*pb.BatchObjectsReply, error)
	queryChange             func([]*pb.SearchResult) []*pb.SearchResult
	distances, hybridScores map[string]float64
	groups, hybrids         []int
	scans                   atomic.Int64
	queryError, sourceError bool
}

func (n *nativeFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/v1/meta":
		_, _ = w.Write([]byte(`{"version":"1.39.8","grpcMaxMessageSize":16777216}`))
	case r.URL.Path == "/v1/.well-known/openid-configuration":
		http.NotFound(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/schema":
		if err := jsonv2.UnmarshalRead(r.Body, &n.wireClass); err != nil {
			n.t.Error(err)
			http.Error(w, "schema", 400)
			return
		}
		_ = jsonv2.MarshalWrite(w, n.wireClass)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/schema/Documents":
		// Weaviate fills these index defaults before returning a stored collection.
		n.wireClass["invertedIndexConfig"] = map[string]any{"bm25": map[string]any{"b": 0.75, "k1": 1.2}}
		n.wireClass["description"] = n.class.Description
		_ = jsonv2.MarshalWrite(w, n.wireClass)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/objects/Documents/"):
		n.deletes.Add(1)
		id := strings.TrimPrefix(r.URL.Path, "/v1/objects/Documents/")
		if _, ok := n.objects[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(n.objects, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		n.t.Errorf("unexpected native HTTP %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}
func (n *nativeFixture) checkGrant(ctx context.Context) {
	var want []string
	if n.apiKey != "" {
		want = []string{"Bearer " + n.apiKey}
	}
	if !slices.Equal(metadata.ValueFromIncomingContext(ctx, "authorization"), want) {
		n.t.Error("wrong gRPC authorization scope")
	}
}
func (n *nativeFixture) BatchObjects(ctx context.Context, req *pb.BatchObjectsRequest) (*pb.BatchObjectsReply, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.checkGrant(ctx)
	n.batches.Add(1)
	reply := &pb.BatchObjectsReply{}
	if n.batchReply != nil {
		var err error
		reply, err = n.batchReply(req)
		if err != nil {
			return nil, err
		}
	}
	rejected := make(map[int32]bool)
	for _, failure := range reply.Errors {
		rejected[failure.Index] = true
	}
	for i, o := range req.Objects {
		if o.Collection != "Documents" || len(o.Vectors) != 1 || o.Vectors[0].Name != vectorName {
			n.t.Errorf("wrong native vector binding: %v", o.Vectors)
		}
		if !rejected[int32(i)] {
			n.objects[o.Uuid] = o
		}
	}
	return reply, nil
}
func (n *nativeFixture) Search(ctx context.Context, req *pb.SearchRequest) (*pb.SearchReply, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.checkGrant(ctx)
	if n.queryError {
		return nil, status.Error(codes.Unavailable, "native query unavailable")
	}
	hybrid := req.HybridSearch != nil
	if !hybrid && req.NearVector == nil {
		n.scans.Add(1)
		if n.sourceError {
			return nil, status.Error(codes.PermissionDenied, "source enumeration not granted")
		}
	}
	if req.Collection != "Documents" || req.Limit == 0 || !slices.Equal(req.Metadata.GetVectors(), []string{vectorName}) {
		n.t.Errorf("query lost collection, requested vector or native limit: %v", req)
	}
	near := req.NearVector
	if hybrid {
		near = req.HybridSearch.NearVector
		// v6.0.0 serializes its public RelativeScore option using the server default.
		if !req.Metadata.GetScore() || req.HybridSearch.Query == "" || !slices.Equal(req.HybridSearch.Properties, []string{fieldContent}) || (req.HybridSearch.FusionType != pb.Hybrid_FUSION_TYPE_RELATIVE_SCORE && req.HybridSearch.FusionType != pb.Hybrid_FUSION_TYPE_UNSPECIFIED) {
			n.t.Errorf("hybrid query lost lexical input, selected fields, native fusion or score: %v", req.HybridSearch)
		}
	} else if !req.Metadata.GetDistance() {
		n.t.Error("missing distance selection")
	}
	if !slices.Equal(near.GetTargets().GetTargetVectors(), []string{vectorName}) || len(near.GetVectorForTargets()) != 1 || near.GetVectorForTargets()[0].Name != vectorName {
		n.t.Errorf("missing named query coordinates: %v", near)
	}
	if !req.Metadata.GetUuid() || !slices.Equal(req.Properties.GetNonRefProperties(), []string{fieldContent, fieldMetadata, fieldTerms}) {
		n.t.Errorf("missing hit identity or storage fields: %v", req)
	}
	var terms []string
	if f := req.Filters; f != nil {
		if f.Operator != pb.Filters_OPERATOR_CONTAINS_ANY || f.GetTarget().GetProperty() != fieldTerms {
			n.t.Errorf("expected native ContainsAny over exact terms: %v", f)
		}
		terms = f.GetValueTextArray().GetValues()
		n.groups = append(n.groups, len(terms))
	}
	if hybrid {
		n.hybrids = append(n.hybrids, len(terms))
	}
	ids := slices.Sorted(maps.Keys(n.objects))
	score := func(id string) float64 {
		if hybrid {
			if v, ok := n.hybridScores[id]; ok {
				return v
			}
			return 1
		}
		return n.distances[id]
	}
	slices.SortStableFunc(ids, func(a, b string) int {
		if hybrid {
			return cmp.Compare(score(b), score(a))
		}
		return cmp.Compare(score(a), score(b))
	})
	var rows []*pb.SearchResult
	for _, id := range ids {
		o := n.objects[id]
		props := o.GetProperties()
		var rowTerms []string
		for _, a := range props.GetTextArrayProperties() {
			if a.PropName == fieldTerms {
				rowTerms = a.Values
			}
		}
		if len(terms) > 0 && !slices.ContainsFunc(terms, func(s string) bool { return slices.Contains(rowTerms, s) }) {
			continue
		}
		fields := make(map[string]*pb.Value)
		for _, key := range []string{fieldMetadata, fieldContent} {
			if v, ok := props.GetNonRefProperties().GetFields()[key]; ok {
				fields[key] = &pb.Value{Kind: &pb.Value_TextValue{TextValue: v.GetStringValue()}}
			}
		}
		fields[fieldTerms] = &pb.Value{Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{Kind: &pb.ListValue_TextValues{TextValues: &pb.TextValues{Values: rowTerms}}}}}
		uid := uuid.MustParse(id)
		meta := &pb.MetadataResult{IdAsBytes: uid[:], Vectors: o.Vectors}
		if hybrid {
			meta.ScorePresent = true
			meta.Score = float32(score(id))
		} else {
			meta.DistancePresent = true
			meta.Distance = float32(score(id))
		}
		rows = append(rows, &pb.SearchResult{Metadata: meta, Properties: &pb.PropertiesResult{NonRefProps: &pb.Properties{Fields: fields}}})
		if len(rows) == int(req.Limit) {
			break
		}
	}
	if n.queryChange != nil {
		rows = n.queryChange(rows)
	}
	return &pb.SearchReply{Results: rows}, nil
}
func nativeClass(name, metric string) *collections.Collection {
	return &collections.Collection{Name: name, Description: fixtureSpace.StorageKey(), Vectors: map[string]collections.VectorConfig{vectorName: {Vectorizer: selfprovided.Vectorizer, Index: vectorindex.HNSW{Distance: vectorindex.Distance(metric), FlatSearchCutoff: 1024}}}, Properties: []collections.Property{
		{Name: fieldContent, DataType: collections.DataTypeText, Tokenization: collections.TokenizationWord},
		{Name: fieldMetadata, DataType: collections.DataTypeText, Tokenization: collections.TokenizationField},
		{Name: fieldTerms, DataType: collections.DataTypeTextArray, Tokenization: collections.TokenizationField, IndexFilterable: new(true), IndexSearchable: new(false)},
	}}
}
func newNativeStore(t *testing.T, n *nativeFixture) *Store {
	t.Helper()
	n.t = t
	n.objects = make(map[string]*pb.BatchObject)
	if n.class == nil {
		n.class = nativeClass("Documents", distanceCosine)
	}
	rest := httptest.NewServer(n)
	t.Cleanup(rest.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	pb.RegisterWeaviateServer(g, n)
	done := make(chan struct{})
	go func() { defer close(done); _ = g.Serve(listener) }()
	t.Cleanup(func() { g.Stop(); <-done })
	origin, err := url.Parse(rest.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(t.Context(), ClientConfig{Endpoint: rest.URL, APIKey: n.apiKey, GRPCAddress: net.JoinHostPort(origin.Hostname(), port)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := c.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	admin := testAdmin(t, origin, port)
	if _, err = admin.Collections.Create(t.Context(), *n.class); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(t.Context(), StoreConfig{Client: c, Collection: "Documents", Space: fixtureSpace})
	if err != nil {
		t.Fatal(fmt.Errorf("construct native store: %w", err))
	}
	return s
}

// The independent SDK provisions disposable test collections anonymously.
// Product calls never delegate authenticated REST to it.
func testAdmin(t *testing.T, origin *url.URL, grpcPort string) *sdk.Client {
	t.Helper()
	port := origin.Port()
	if port == "" {
		if origin.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	c, err := sdk.NewClient(t.Context(), sdk.WithHost(origin.Hostname()), sdk.WithScheme(origin.Scheme), sdk.WithHTTPPort(port), sdk.WithGRPCPort(grpcPort))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := c.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return c
}

const fixtureSpace vectorstore.SpaceID = "paired-v1"

func fixtureVector(string) []float64 { return []float64{1, 0} }
func fixtureBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: fixtureSpace}
	for _, doc := range docs {
		b.Records = append(b.Records, vectorstore.Record{Document: doc, Values: fixtureVector(doc.Text)})
	}
	return b
}
func fixtureQuery(ctx context.Context, s *Store, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	if q.Options.EffectiveMode() == vectorstore.SearchModeHybrid {
		q.LexicalText = "native text"
	}
	return s.Query(ctx, q)
}
