package etl_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

type boundaryCounter func(context.Context, string) (int, error)

func (b boundaryCounter) CountText(ctx context.Context, text string) (int, error) {
	return b(ctx, text)
}

func TestBatcherMeasuresFormattedContentAndPreservesErrors(t *testing.T) {
	cause := errors.New("format failed")
	for _, failure := range []bool{false, true} {
		counterCalls := 0
		batcher, err := etl.NewTokenCountBatcher(etl.TokenCountBatcherConfig{
			MaxTokens: 10,
			Counter: boundaryCounter(func(_ context.Context, text string) (int, error) {
				counterCalls++
				if text != "rendered" {
					t.Fatalf("counter received %q", text)
				}
				return len(text), nil
			}),
			Formatter: document.FormatterFunc(func(*document.Document) (string, error) {
				if failure {
					return "partial", cause
				}
				return "rendered", nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		doc := &document.Document{Text: "raw"}
		batches, err := batcher.Batch(t.Context(), []*document.Document{doc})
		if failure {
			if batches != nil || !errors.Is(err, cause) || counterCalls != 0 {
				t.Fatalf("failed format: %v, %v, calls=%d", batches, err, counterCalls)
			}
		} else if err != nil || len(batches) != 1 || len(batches[0]) != 1 || batches[0][0] != doc || counterCalls != 1 {
			t.Fatalf("successful format: %v, %v, calls=%d", batches, err, counterCalls)
		}
	}
}

func TestBatcherRejectsCancellationDuringMeasurement(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	batcher, err := etl.NewTokenCountBatcher(etl.TokenCountBatcherConfig{
		MaxTokens: 10,
		Counter: boundaryCounter(func(context.Context, string) (int, error) {
			cancel()
			return 1, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if batches, err := batcher.Batch(ctx, []*document.Document{{Text: "text"}}); batches != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Batch = %v, %v", batches, err)
	}
}

func TestBatcherRejectsInvalidDocumentsBeforeMeasuring(t *testing.T) {
	batcher, err := etl.NewTokenCountBatcher(etl.TokenCountBatcherConfig{
		Counter: boundaryCounter(func(context.Context, string) (int, error) {
			t.Fatal("invalid document reached token counter")
			return 0, nil
		}),
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if batches, err := batcher.Batch(t.Context(), []*document.Document{{Text: "bad\xff"}}); batches != nil || !errors.Is(err, document.ErrInvalidDocument) {
		t.Fatalf("Batch = %v, %v", batches, err)
	}
}

func TestBatcherRejectsTypedNilFormatter(t *testing.T) {
	var formatter *etl.MetadataHeaderFormatter
	batcher, err := etl.NewTokenCountBatcher(etl.TokenCountBatcherConfig{Counter: textLengthCounter{}, MaxTokens: 10, Formatter: formatter})
	if batcher != nil || err == nil {
		t.Fatalf("NewTokenCountBatcher = %v, %v", batcher, err)
	}
}
