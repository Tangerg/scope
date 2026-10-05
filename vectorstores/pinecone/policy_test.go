package pinecone

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pineconesdk "github.com/pinecone-io/go-pinecone/v4/pinecone"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/Tangerg/scope/core/vectorstore"
)

func declaredIndex() *pineconesdk.Index {
	return &pineconesdk.Index{Name: "documents", Host: "native.fixture", Metric: pineconesdk.Cosine, VectorType: "dense", Dimension: new(int32(2)), Status: &pineconesdk.IndexStatus{Ready: true}, Spec: &pineconesdk.IndexSpec{Serverless: &pineconesdk.ServerlessSpec{Cloud: pineconesdk.Aws, Region: "us-east-1"}}}
}

func TestNativePolicyHasNoConfiguredCompetitorOrDefault(t *testing.T) {
	for _, metric := range []pineconesdk.IndexMetric{pineconesdk.Cosine, pineconesdk.Dotproduct, pineconesdk.Euclidean} {
		index := declaredIndex()
		index.Metric = metric
		var schema nativeSchema
		if err := schema.read(index, index.Name); err != nil || schema.dimensions != 2 || schema.metric != metric {
			t.Fatalf("schema=%v error=%v", schema, err)
		}
		index.Host = "https://native.fixture"
		if err := schema.read(index, index.Name); err != nil {
			t.Fatal(err)
		}
	}
	for name, mutate := range map[string]func(*pineconesdk.Index){
		"wrong name":          func(index *pineconesdk.Index) { index.Name = "other" },
		"missing host":        func(index *pineconesdk.Index) { index.Host = "" },
		"invalid host":        func(index *pineconesdk.Index) { index.Host = "http://:1234" },
		"credentials":         func(index *pineconesdk.Index) { index.Host = "https://user@native.fixture" },
		"host path":           func(index *pineconesdk.Index) { index.Host = "native.fixture/path" },
		"missing dimension":   func(index *pineconesdk.Index) { index.Dimension = nil },
		"zero dimension":      func(index *pineconesdk.Index) { index.Dimension = new(int32(0)) },
		"too many dimensions": func(index *pineconesdk.Index) { index.Dimension = new(int32(20001)) },
		"unknown metric":      func(index *pineconesdk.Index) { index.Metric = "unknown" },
		"missing vector type": func(index *pineconesdk.Index) { index.VectorType = "" },
		"sparse type":         func(index *pineconesdk.Index) { index.VectorType = "sparse" },
		"missing status":      func(index *pineconesdk.Index) { index.Status = nil },
		"not ready":           func(index *pineconesdk.Index) { index.Status.Ready = false },
		"missing spec":        func(index *pineconesdk.Index) { index.Spec = nil },
		"pod index":           func(index *pineconesdk.Index) { index.Spec.Serverless = nil; index.Spec.Pod = &pineconesdk.PodSpec{} },
	} {
		t.Run(name, func(t *testing.T) {
			index := declaredIndex()
			mutate(index)
			var schema nativeSchema
			if err := schema.read(index, "documents"); !errors.Is(err, ErrIncompatibleIndex) {
				t.Fatal(err)
			}
		})
	}
	var schema nativeSchema
	if err := schema.read(nil, "documents"); !errors.Is(err, ErrIncompatibleIndex) {
		t.Fatal(err)
	}
}

func TestNativeScoresDelegateOnlyNormalizationToCore(t *testing.T) {
	for _, sample := range []struct {
		metric pineconesdk.IndexMetric
		raw    float64
		want   vectorstore.Score
	}{
		{pineconesdk.Cosine, 0, vectorstore.ScoreFromCosineSimilarity(0)},
		{pineconesdk.Dotproduct, 10, vectorstore.ScoreFromInnerProduct(10)},
		{pineconesdk.Euclidean, 4, vectorstore.ScoreFromDistance(4)},
	} {
		score, err := (nativeSchema{metric: sample.metric}).score(sample.raw)
		if err != nil || score != sample.want {
			t.Fatalf("score=%v error=%v", score, err)
		}
	}
	for _, raw := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := (nativeSchema{metric: pineconesdk.Cosine}).score(raw); err == nil {
			t.Fatal("nonfinite native score succeeded")
		}
	}
	if _, err := (nativeSchema{}).score(1); err == nil {
		t.Fatal("missing metric succeeded")
	}
}

type observedClient struct {
	client        *pineconesdk.Client
	connection    *pineconesdk.IndexConnection
	openErr       error
	nilConnection bool
}

func (o *observedClient) DescribeIndex(ctx context.Context, name string) (*pineconesdk.Index, error) {
	return o.client.DescribeIndex(ctx, name)
}

func (o *observedClient) Index(params pineconesdk.NewIndexConnParams, options ...grpc.DialOption) (*pineconesdk.IndexConnection, error) {
	if o.openErr != nil || o.nilConnection {
		return nil, o.openErr
	}
	var err error
	o.connection, err = o.client.Index(params, options...)
	return o.connection, err
}

func TestConstructionUsesNativeSDKPolicyAndOwnsOnlyOpenedConnection(t *testing.T) {
	for _, failure := range []string{"", "list denied", "open error", "nil connection"} {
		t.Run(failure, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
				method, _ := grpc.MethodFromServerStream(stream)
				if method != "/VectorService/List" {
					return status.Error(codes.Unimplemented, "unexpected operation")
				}
				if receiveErr := stream.RecvMsg(&emptypb.Empty{}); receiveErr != nil {
					return receiveErr
				}
				if failure == "list denied" {
					return status.Error(codes.PermissionDenied, "denied")
				}
				return stream.SendMsg(&emptypb.Empty{})
			}))
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			t.Cleanup(func() {
				server.Stop()
				if serveErr := <-done; serveErr != nil {
					t.Error(serveErr)
				}
			})
			index := declaredIndex()
			index.Host = "http://" + listener.Addr().String()
			payload, err := jsonv2.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/indexes/documents" || r.Header.Get("X-Pinecone-Api-Version") != "2025-04" {
					t.Errorf("unexpected native policy request: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(payload)
			}))
			defer httpServer.Close()
			client, err := pineconesdk.NewClient(pineconesdk.NewClientParams{ApiKey: "fixture", Host: httpServer.URL, RestClient: httpServer.Client()})
			if err != nil {
				t.Fatal(err)
			}
			observed := &observedClient{client: client, nilConnection: failure == "nil connection"}
			if failure == "open error" {
				observed.openErr = errNativeFailure
			}
			store, err := NewStore(t.Context(), StoreConfig{Client: observed, IndexName: index.Name, EmbeddingModel: fixtureModel(), DocumentBatcher: fixtureBatcher{}})
			if failure == "" {
				if err != nil || store == nil {
					t.Fatalf("store=%v error=%v", store, err)
				}
				if observed.connection.Namespace() != "__default__" {
					t.Fatal("Scope changed native default namespace")
				}
				if err = store.Close(); err != nil {
					t.Fatal(err)
				}
			} else if err == nil || store != nil {
				t.Fatalf("failure invented a usable store: %v, %v", store, err)
			}
			if observed.connection != nil {
				if _, err = observed.connection.ListVectors(t.Context(), &pineconesdk.ListVectorsRequest{}); err == nil {
					t.Fatal("opened connection remained live after close or constructor failure")
				}
			}
			if _, err = client.DescribeIndex(t.Context(), "documents"); err != nil {
				t.Fatalf("Store closed host client: %v", err)
			}
		})
	}
}

func TestConstructionReportsControlFailuresAndConfigErrors(t *testing.T) {
	for _, code := range []int{401, 403, 404, 500} {
		httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
		client, err := pineconesdk.NewClient(pineconesdk.NewClientParams{ApiKey: "fixture", Host: httpServer.URL, RestClient: httpServer.Client()})
		if err != nil {
			t.Fatal(err)
		}
		config := StoreConfig{Client: client, IndexName: "documents", EmbeddingModel: fixtureModel(), DocumentBatcher: fixtureBatcher{}}
		store, err := NewStore(t.Context(), config)
		httpServer.Close()
		if err == nil || store != nil {
			t.Fatalf("HTTP %d invented native policy", code)
		}
	}
	var typedNil *observedClient
	valid := StoreConfig{Client: typedNil, IndexName: "documents", EmbeddingModel: fixtureModel(), DocumentBatcher: fixtureBatcher{}}
	if err := valid.Validate(); !errors.Is(err, ErrMissingClient) {
		t.Fatal(err)
	}
	valid.Client = &observedClient{}
	for _, mutate := range []func(*StoreConfig){
		func(config *StoreConfig) { config.IndexName = "" },
		func(config *StoreConfig) { config.EmbeddingModel = nil },
		func(config *StoreConfig) { config.DocumentBatcher = nil },
		func(config *StoreConfig) { config.Namespace = "🙂" },
		func(config *StoreConfig) { config.Namespace = strings.Repeat("x", 513) },
	} {
		config := valid
		mutate(&config)
		if store, err := NewStore(t.Context(), config); err == nil || store != nil {
			t.Fatal("invalid config reached native client")
		}
	}
}
