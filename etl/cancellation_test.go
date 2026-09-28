package etl_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

func TestDocumentStagesHonorCancellationWithoutDocuments(t *testing.T) {
	splitter, err := etl.NewTextSplitter(etl.TextSplitterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	assigner, err := etl.NewIDAssigner(etl.IDAssignerConfig{Generator: etl.UUIDGenerator{}})
	if err != nil {
		t.Fatal(err)
	}
	batcher, err := etl.NewTokenCountBatcher(etl.TokenCountBatcherConfig{Counter: textLengthCounter{}, MaxTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, docs := range [][]*document.Document{nil, {}} {
		if _, err := splitter.Split(ctx, docs); !errors.Is(err, context.Canceled) {
			t.Errorf("Split error = %v, want context.Canceled", err)
		}
		if _, err := assigner.Assign(ctx, docs); !errors.Is(err, context.Canceled) {
			t.Errorf("Assign error = %v, want context.Canceled", err)
		}
		if _, err := batcher.Batch(ctx, docs); !errors.Is(err, context.Canceled) {
			t.Errorf("Batch error = %v, want context.Canceled", err)
		}
	}
}
