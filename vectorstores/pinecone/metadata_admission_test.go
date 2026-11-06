package pinecone

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestMetadataAdmissionMatchesCompleteSDKMetadata(t *testing.T) {
	point, err := encodeRecord(&document.Document{ID: "document", Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	textBytes := sort.Search(metadataByteBudget, func(length int) bool {
		point.Metadata.Fields[contentField] = structpb.NewStringValue(strings.Repeat("x", length+1))
		return proto.Size(point.Metadata) > metadataByteBudget
	})
	for _, text := range []string{strings.Repeat("x", textBytes), strings.Repeat("界", textBytes/3) + strings.Repeat("x", textBytes%3)} {
		t.Run(fmt.Sprintf("first rune %q", []rune(text)[0]), func(t *testing.T) {
			store, native := fixtureStore(t)
			calls := 0
			store.index = sdkUpsertConnection(t, "fixture", func(message proto.Message) {
				calls++
				request := message.ProtoReflect()
				vector := request.Get(request.Descriptor().Fields().ByName("vectors")).List().Get(0).Message()
				facts := vector.Get(vector.Descriptor().Fields().ByName("metadata")).Message().Interface()
				if size := proto.Size(facts); size != metadataByteBudget {
					t.Errorf("actual SDK metadata=%d bytes, want %d", size, metadataByteBudget)
				}
			})
			docs := []*document.Document{{ID: "document", Text: text}}
			if err := store.CheckDocuments(t.Context(), fixtureSpace, docs); err != nil {
				t.Fatal(err)
			}
			if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil || calls != 1 {
				t.Fatalf("upsert=%v RPCs=%d", err, calls)
			}
			docs[0].Text += "x"
			describes := native.describes
			if err := store.CheckDocuments(t.Context(), fixtureSpace, docs); !errors.Is(err, vectorstore.ErrInvalidDocument) || native.describes != describes {
				t.Fatalf("admission=%v control RPCs=%d", err, native.describes-describes)
			}
			if err := store.Upsert(t.Context(), fixtureBatch(docs)); !errors.Is(err, vectorstore.ErrInvalidDocument) || calls != 1 || native.describes != describes {
				t.Fatalf("upsert=%v data RPCs=%d control RPCs=%d", err, calls, native.describes-describes)
			}
		})
	}
}

func TestOversizedLaterMetadataPreventsEveryNativeWrite(t *testing.T) {
	largeFacts, err := metadata.FromValues(map[string]any{"tenant": strings.Repeat("m", 20_000)})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		last *document.Document
	}{
		{"content", &document.Document{ID: "last", Text: strings.Repeat("x", 50<<10)}},
		{"facts plus keyword projection", &document.Document{ID: "last", Text: "text", Metadata: largeFacts}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, native := fixtureStore(t)
			docs := make([]*document.Document, MaxVectorsPerUpsert+1)
			for index := range MaxVectorsPerUpsert {
				docs[index] = &document.Document{ID: fmt.Sprintf("doc-%d", index), Text: "valid"}
			}
			docs[len(docs)-1] = test.last
			if err := store.CheckDocuments(t.Context(), fixtureSpace, docs); !errors.Is(err, vectorstore.ErrInvalidDocument) || native.describes != 0 {
				t.Fatalf("admission=%v control RPCs=%d", err, native.describes)
			}
			if err := store.Upsert(t.Context(), fixtureBatch(docs)); !errors.Is(err, vectorstore.ErrInvalidDocument) || len(native.upserts) != 0 || native.describes != 0 {
				t.Fatalf("upsert=%v writes=%v control RPCs=%d", err, native.upserts, native.describes)
			}
		})
	}
}
