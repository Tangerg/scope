package pinecone

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	pineconesdk "github.com/pinecone-io/go-pinecone/v7/pinecone"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

func sdkUpsertConnection(t *testing.T, namespace string, observe func(proto.Message)) *pineconesdk.IndexConnection {
	t.Helper()
	client, err := pineconesdk.NewClient(pineconesdk.NewClientParams{ApiKey: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := client.Index(pineconesdk.NewIndexConnParams{Host: "http://pinecone.invalid", Namespace: namespace},
		grpc.WithUnaryInterceptor(func(_ context.Context, _ string, request, response any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
			message := request.(proto.Message)
			observe(message)
			input := message.ProtoReflect()
			count := input.Get(input.Descriptor().Fields().ByName("vectors")).List().Len()
			output := response.(proto.Message).ProtoReflect()
			output.Set(output.Descriptor().Fields().ByName("upserted_count"), protoreflect.ValueOfUint32(uint32(count)))
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	return connection
}

func TestUpsertBatchesRespectEncodedSDKRequestBudget(t *testing.T) {
	for _, namespace := range []string{"", strings.Repeat("n", 512)} {
		t.Run(fmt.Sprintf("namespace bytes %d", len(namespace)), func(t *testing.T) {
			store, _ := fixtureStore(t)
			var actualIDs []string
			var groups int
			store.index = sdkUpsertConnection(t, namespace, func(message proto.Message) {
				groups++
				if size := proto.Size(message); size > 2_000_000 {
					t.Errorf("SDK upsert encoded %d bytes, budget is 2000000", size)
				}
				request := message.ProtoReflect()
				if got := request.Get(request.Descriptor().Fields().ByName("namespace")).String(); got != namespace {
					t.Errorf("namespace = %q, want %q", got, namespace)
				}
				vectors := request.Get(request.Descriptor().Fields().ByName("vectors")).List()
				if vectors.Len() > 1000 {
					t.Errorf("SDK upsert has %d vectors, maximum is 1000", vectors.Len())
				}
				for index := range vectors.Len() {
					vector := vectors.Get(index).Message()
					actualIDs = append(actualIDs, vector.Get(vector.Descriptor().Fields().ByName("id")).String())
				}
			})
			docs := make([]*document.Document, 1000)
			wantIDs := make([]string, len(docs))
			for index := range docs {
				wantIDs[index] = fmt.Sprintf("doc-%04d", index)
				docs[index] = &document.Document{ID: wantIDs[index], Text: strings.Repeat("x", 4096)}
			}
			if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
				t.Fatal(err)
			}
			if groups != 3 || !slices.Equal(actualIDs, wantIDs) {
				t.Fatalf("groups=%d, upserted IDs match input=%v", groups, slices.Equal(actualIDs, wantIDs))
			}
		})
	}
}

func TestDenseVectorWireSizeMatchesSDK(t *testing.T) {
	for _, dimensions := range []int{2, 128, 20000} {
		t.Run(fmt.Sprint(dimensions), func(t *testing.T) {
			facts, err := metadata.FromValues(map[string]any{"source": "quoted \"text\"", "language": "中文"})
			if err != nil {
				t.Fatal(err)
			}
			point, err := encodeRecord(&document.Document{ID: "document", Text: "\nquoted \"text\" 中文", Metadata: facts})
			if err != nil {
				t.Fatal(err)
			}
			point.Values = new(make([]float32, dimensions))
			(*point.Values)[0] = 1
			connection := sdkUpsertConnection(t, "namespace", func(message proto.Message) {
				request := message.ProtoReflect()
				native := request.Get(request.Descriptor().Fields().ByName("vectors")).List().Get(0).Message().Interface()
				measured, err := denseVectorWireSize(point)
				if err != nil || measured != proto.Size(native) {
					t.Fatalf("measured=%d, SDK=%d, error=%v", measured, proto.Size(native), err)
				}
			})
			if _, err := connection.UpsertVectors(t.Context(), []*pineconesdk.Vector{point}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestByteLimitedUpsertPreservesWriteAcknowledgments(t *testing.T) {
	store, native := fixtureStore(t)
	docs := make([]*document.Document, 1000)
	ids := make([]string, len(docs))
	for index := range docs {
		ids[index] = fmt.Sprintf("doc-%04d", index)
		docs[index] = &document.Document{ID: ids[index], Text: strings.Repeat("x", 4096)}
	}
	native.upsert = func(points []*pineconesdk.Vector) (uint32, error) {
		if len(native.upserts) == 2 {
			return 1, nil
		}
		return uint32(len(points)), nil
	}
	err := store.Upsert(t.Context(), fixtureBatch(docs))
	failure, ok := errors.AsType[*vectorstore.WriteError](err)
	if !ok || len(native.upserts) != 2 {
		t.Fatalf("upsert calls=%d, error=%v", len(native.upserts), err)
	}
	acknowledgedEnd := len(native.upserts[0])
	unconfirmedEnd := acknowledgedEnd + len(native.upserts[1])
	if unconfirmedEnd >= len(ids) || !slices.Equal(failure.Outcome().Acknowledged, ids[:acknowledgedEnd]) ||
		!slices.Equal(failure.Outcome().Unconfirmed, ids[acknowledgedEnd:unconfirmedEnd]) ||
		!slices.Equal(failure.Outcome().Unattempted, ids[unconfirmedEnd:]) ||
		failure.Outcome().AcknowledgedCount == nil || *failure.Outcome().AcknowledgedCount != 1 {
		t.Fatalf("byte-limited write acknowledgment = %+v", failure)
	}
}
