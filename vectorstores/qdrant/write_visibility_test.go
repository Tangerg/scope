package qdrant

import (
	"context"
	"testing"

	"github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
)

type visibilityBatcher struct{}

func (visibilityBatcher) Batch(
	ctx context.Context,
	documents []*document.Document,
) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

// Qdrant acknowledges an update once it reaches the write-ahead log, so a
// request that does not wait lets a following Search observe stale points.
// Every write the store builds must ask to wait.
func TestWriteRequestsWaitForApplication(t *testing.T) {
	t.Parallel()

	store := &Store{collectionName: "documents"}
	t.Run("delete by filter", func(t *testing.T) {
		request := store.buildDeletePoints(qdrant.NewPointsSelectorFilter(&qdrant.Filter{}))
		assertWaits(t, request.Wait)
	})
	t.Run("delete by id", func(t *testing.T) {
		request := store.buildDeletePoints(qdrant.NewPointsSelector(qdrant.NewIDNum(1)))
		assertWaits(t, request.Wait)
	})
	t.Run("upsert", func(t *testing.T) {
		client, err := embeddingclient.New(embedding.ModelFunc(
			func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
				outputs := make([]*embedding.Output, len(request.Texts))
				for index := range outputs {
					outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
				}
				return embedding.NewResponse(outputs, nil)
			}))
		if err != nil {
			t.Fatal(err)
		}
		upserting := &Store{
			collectionName:  "documents",
			embeddingClient: client,
			documentBatcher: visibilityBatcher{},
		}
		request, err := upserting.buildUpsertPoints(t.Context(), &vectorstore.IndexRequest{
			Documents: []*document.Document{{ID: "1", Text: "first"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		assertWaits(t, request.Wait)
	})
}

func assertWaits(t *testing.T, wait *bool) {
	t.Helper()
	if wait == nil {
		t.Fatal("write request does not ask Qdrant to wait for application")
	}
	if !*wait {
		t.Fatal("write request explicitly declines to wait for application")
	}
}
