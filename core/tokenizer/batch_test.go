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

func mustLimit(t *testing.T, maximum int, counter tokenizer.TextCounter) tokenizer.Limit {
	t.Helper()
	limit, err := tokenizer.NewLimit(maximum, counter)
	if err != nil {
		t.Fatal(err)
	}
	return limit
}

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
	groups, err := (tokenizer.BatchConfig{Tokens: mustLimit(t, math.MaxInt, cost), MaxItems: 2}).Batches(t.Context(), input)
	if err != nil || !reflect.DeepEqual(groups, [][]string{{"A"}, {"B", "zero"}, {"C"}}) {
		t.Fatalf("groups=%v err=%v", groups, err)
	}
	cause := errors.New("counter offline")
	partial, err := (tokenizer.BatchConfig{Tokens: mustLimit(t, 2, costFunc(func(_ context.Context, s string) (int, error) {
		if s == "C" {
			return 0, cause
		}
		return 2, nil
	}))}).Batches(t.Context(), input)
	if partial != nil || !errors.Is(err, cause) {
		t.Fatalf("partial output=%v err=%v", partial, err)
	}
}

func TestBadLimitsOrCancellationPublishNoGroups(t *testing.T) {
	var nilCounter costFunc
	free := costFunc(func(context.Context, string) (int, error) { return 0, nil })
	for _, limit := range []struct {
		maximum int
		counter tokenizer.TextCounter
	}{{-1, free}, {0, free}, {1, nil}, {1, nilCounter}} {
		if _, err := tokenizer.NewLimit(limit.maximum, limit.counter); !errors.Is(err, tokenizer.ErrInvalidLimit) {
			t.Fatalf("NewLimit(%d, %v) error = %v", limit.maximum, limit.counter, err)
		}
	}
	if groups, err := (tokenizer.BatchConfig{MaxItems: -1}).Batches(t.Context(), []string{"first"}); groups != nil || err == nil {
		t.Fatalf("bad item limit admitted a batch: %v %v", groups, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	groups, err := (tokenizer.BatchConfig{MaxItems: 1}).Batches(ctx, []string{"first", "second"})
	if groups != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled batch: %v %v", groups, err)
	}
	for _, cost := range []int{-1, 2} {
		cfg := tokenizer.BatchConfig{Tokens: mustLimit(t, 1, costFunc(func(context.Context, string) (int, error) { return cost, nil }))}
		if groups, err := cfg.Batches(t.Context(), []string{"one"}); groups != nil || err == nil {
			t.Fatal(groups, err)
		}
	}
}
