package tokenizer_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/tokenizer"
)

type costFunc func(context.Context, string) (int, error)

func (c costFunc) CountText(ctx context.Context, text string) (int, error) { return c(ctx, text) }
func TestBatchLimitsNeverWrapOrLoseAnItem(t *testing.T) {
	input := []string{"A", "B", "zero", "C"}
	cost := costFunc(func(_ context.Context, s string) (int, error) {
		switch s {
		case "B":
			return math.MaxInt, nil
		case "zero":
			return 0, nil
		default:
			return 1, nil
		}
	})
	groups, err := (tokenizer.BatchConfig{MaxTokens: math.MaxInt, Counter: cost, MaxItems: 2}).Batches(t.Context(), input)
	if err != nil || !reflect.DeepEqual(groups, [][]string{{"A"}, {"B", "zero"}, {"C"}}) {
		t.Fatalf("groups=%v err=%v", groups, err)
	}
	cause := errors.New("counter offline")
	partial, err := (tokenizer.BatchConfig{MaxTokens: 2, Counter: costFunc(func(_ context.Context, s string) (int, error) {
		if s == "C" {
			return 0, cause
		}
		return 2, nil
	})}).Batches(t.Context(), input)
	if partial != nil || !errors.Is(err, cause) {
		t.Fatalf("partial output=%v err=%v", partial, err)
	}
}

func TestBadLimitsOrCancellationPublishNoGroups(t *testing.T) {
	var nilCounter costFunc
	for _, cfg := range []tokenizer.BatchConfig{{MaxItems: -1}, {MaxTokens: -1}, {MaxTokens: 1}, {Counter: costFunc(func(context.Context, string) (int, error) { return 0, nil })}, {MaxTokens: 1, Counter: nilCounter}} {
		groups, err := cfg.Batches(t.Context(), []string{"first"})
		if groups != nil || err == nil {
			t.Fatalf("bad limits admitted a batch: %v %v", groups, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	groups, err := (tokenizer.BatchConfig{MaxItems: 1}).Batches(ctx, []string{"first", "second"})
	if groups != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled batch: %v %v", groups, err)
	}
	for _, cost := range []int{-1, 2} {
		cfg := tokenizer.BatchConfig{MaxTokens: 1, Counter: costFunc(func(context.Context, string) (int, error) { return cost, nil })}
		if groups, err := cfg.Batches(t.Context(), []string{"one"}); groups != nil || err == nil {
			t.Fatal(groups, err)
		}
	}
}
