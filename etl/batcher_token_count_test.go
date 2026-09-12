package etl_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

type textLengthEstimator struct{}

func (textLengthEstimator) CountText(_ context.Context, text string) (int, error) {
	return len(text), nil
}

func TestTokenCountBatcherDefaultsToPlainTextWithoutReserve(t *testing.T) {
	batcher, err := etl.NewTokenCountBatcher(etl.TokenCountBatcherConfig{
		Estimator: textLengthEstimator{},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := document.NewDocument("12345", nil)
	second, _ := document.NewDocument("67890", nil)

	batches, err := batcher.Batch(t.Context(), []*document.Document{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || len(batches[0]) != 2 {
		t.Fatalf("batches = %#v, want one full 10-token batch", batches)
	}
}

func TestTokenCountBatcherReserveReducesBudget(t *testing.T) {
	batcher, err := etl.NewTokenCountBatcher(etl.TokenCountBatcherConfig{
		Estimator: textLengthEstimator{},
		MaxTokens: 10,
		Reserve:   0.2,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := document.NewDocument("12345", nil)
	second, _ := document.NewDocument("67890", nil)

	batches, err := batcher.Batch(t.Context(), []*document.Document{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("batch count = %d, want 2 with an 8-token budget", len(batches))
	}
}

func TestTokenCountBatcherValidatesConstructorInput(t *testing.T) {
	tests := []struct {
		name   string
		config etl.TokenCountBatcherConfig
	}{
		{name: "estimator required"},
		{name: "maximum required", config: etl.TokenCountBatcherConfig{
			Estimator: textLengthEstimator{},
		}},
		{name: "negative max", config: etl.TokenCountBatcherConfig{
			Estimator: textLengthEstimator{}, MaxTokens: -1,
		}},
		{name: "invalid reserve", config: etl.TokenCountBatcherConfig{
			Estimator: textLengthEstimator{}, MaxTokens: 10, Reserve: 1,
		}},
		{name: "not a number reserve", config: etl.TokenCountBatcherConfig{
			Estimator: textLengthEstimator{}, MaxTokens: 10, Reserve: math.NaN(),
		}},
		{name: "infinite reserve", config: etl.TokenCountBatcherConfig{
			Estimator: textLengthEstimator{}, MaxTokens: 10, Reserve: math.Inf(1),
		}},
		{name: "reserve removes capacity", config: etl.TokenCountBatcherConfig{
			Estimator: textLengthEstimator{}, MaxTokens: 1, Reserve: 0.5,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := etl.NewTokenCountBatcher(test.config); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

type failingEstimator struct{ err error }

func (f failingEstimator) CountText(context.Context, string) (int, error) {
	return 0, f.err
}

type negativeEstimator struct{}

func (negativeEstimator) CountText(context.Context, string) (int, error) { return -1, nil }

func TestTokenCountBatcherPropagatesEstimatorError(t *testing.T) {
	want := errors.New("estimate failed")
	batcher, err := etl.NewTokenCountBatcher(etl.TokenCountBatcherConfig{
		Estimator: failingEstimator{err: want},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := document.NewDocument("text", nil)
	if _, err := batcher.Batch(t.Context(), []*document.Document{doc}); !errors.Is(err, want) {
		t.Fatalf("Batch error = %v, want %v", err, want)
	}
}

func TestTokenCountBatcherRejectsInvalidStageValues(t *testing.T) {
	batcher, err := etl.NewTokenCountBatcher(etl.TokenCountBatcherConfig{
		Estimator: textLengthEstimator{},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, batchErr := batcher.Batch(t.Context(), []*document.Document{nil}); !errors.Is(batchErr, etl.ErrNilDocument) {
		t.Fatalf("Batch(nil document) error = %v, want ErrNilDocument", batchErr)
	}

	batcher, err = etl.NewTokenCountBatcher(etl.TokenCountBatcherConfig{
		Estimator: negativeEstimator{},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := document.NewDocument("text", nil)
	if _, err := batcher.Batch(t.Context(), []*document.Document{doc}); err == nil {
		t.Fatal("negative token estimate was accepted")
	}
}
