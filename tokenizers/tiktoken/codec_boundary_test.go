package tiktoken

import (
	"context"
	"errors"
	"testing"
)

type boundaryCodec struct {
	err    error
	cancel context.CancelFunc
}

func (b boundaryCodec) Count(string) (int, error) {
	if b.cancel != nil {
		b.cancel()
	}
	return 1, b.err
}

func (b boundaryCodec) Encode(string) ([]uint, []string, error) {
	if b.cancel != nil {
		b.cancel()
	}
	return []uint{1}, []string{"text"}, b.err
}

func (b boundaryCodec) Decode([]uint) (string, error) {
	if b.cancel != nil {
		b.cancel()
	}
	return "text", b.err
}

func TestCodecErrorsPreserveTheirCauseAndDiscardOutput(t *testing.T) {
	cause := errors.New("vocabulary unavailable")
	tokenizer := Tokenizer{encoding: boundaryCodec{err: cause}}
	if count, err := tokenizer.CountText(t.Context(), "text"); count != 0 || !errors.Is(err, cause) {
		t.Fatalf("CountText = %d, %v", count, err)
	}
	if tokens, err := tokenizer.Encode(t.Context(), "text"); tokens != nil || !errors.Is(err, cause) {
		t.Fatalf("Encode = %v, %v", tokens, err)
	}
	if text, err := tokenizer.Decode(t.Context(), []int{1}); text != "" || !errors.Is(err, cause) {
		t.Fatalf("Decode = %q, %v", text, err)
	}
}

func TestCancellationDuringCodecWorkDiscardsOutput(t *testing.T) {
	for _, operation := range []string{"count", "encode", "decode"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tokenizer := Tokenizer{encoding: boundaryCodec{cancel: cancel}}
			switch operation {
			case "count":
				if count, err := tokenizer.CountText(ctx, "text"); count != 0 || !errors.Is(err, context.Canceled) {
					t.Fatalf("CountText = %d, %v", count, err)
				}
			case "encode":
				if tokens, err := tokenizer.Encode(ctx, "text"); tokens != nil || !errors.Is(err, context.Canceled) {
					t.Fatalf("Encode = %v, %v", tokens, err)
				}
			case "decode":
				if text, err := tokenizer.Decode(ctx, []int{1}); text != "" || !errors.Is(err, context.Canceled) {
					t.Fatalf("Decode = %q, %v", text, err)
				}
			}
		})
	}
}

func TestZeroTokenizerCannotDecodeAnEmptyInput(t *testing.T) {
	if text, err := (Tokenizer{}).Decode(t.Context(), nil); text != "" || !errors.Is(err, ErrUninitialized) {
		t.Fatalf("Decode = %q, %v", text, err)
	}
}
