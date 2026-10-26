package etl_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

func TestSplitterRejectsFailedOrCanceledPolicyWithoutPublishingChunks(t *testing.T) {
	cause := errors.New("split policy failed")
	for _, cancellation := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		splitter, err := etl.NewSplitter(etl.SplitterConfig{SplitFunc: func(context.Context, string) ([]string, error) {
			if cancellation {
				cancel()
				return []string{"partial"}, nil
			}
			return []string{"partial"}, cause
		}})
		if err != nil {
			t.Fatal(err)
		}
		want := cause
		if cancellation {
			want = context.Canceled
		}
		if chunks, err := splitter.SplitText(ctx, "source"); chunks != nil || !errors.Is(err, want) {
			t.Fatalf("SplitText = %q, %v", chunks, err)
		}
	}
}

func TestSplitterRejectsInvalidPolicyOutputAndInvalidInputs(t *testing.T) {
	splitter, err := etl.NewSplitter(etl.SplitterConfig{SplitFunc: func(context.Context, string) ([]string, error) {
		return []string{"good", "bad\xff"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, docs := range [][]*document.Document{
		{{Text: "valid source"}},
		{{Text: "bad\xff"}},
	} {
		if chunks, err := splitter.Split(t.Context(), docs); chunks != nil || !errors.Is(err, document.ErrInvalidDocument) {
			t.Fatalf("Split = %v, %v", chunks, err)
		}
	}
	if splitter, err := etl.NewSplitter(etl.SplitterConfig{}); splitter != nil || err == nil {
		t.Fatalf("missing policy = %v, %v", splitter, err)
	}
}
