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

func (b boundaryCodec) GetName() string { return "boundary" }

func (b boundaryCodec) Count(string) (int, error) { return 1, b.err }

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
	if tokens, err := tokenizer.Encode(t.Context(), "text"); tokens != nil || !errors.Is(err, cause) {
		t.Fatalf("Encode = %v, %v", tokens, err)
	}
	if text, err := tokenizer.Decode(t.Context(), []int{1}); text != "" || !errors.Is(err, cause) {
		t.Fatalf("Decode = %q, %v", text, err)
	}
}

func TestCancellationDuringCodecWorkDiscardsOutput(t *testing.T) {
	for _, operation := range []string{"encode", "decode"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tokenizer := Tokenizer{encoding: boundaryCodec{cancel: cancel}}
			if operation == "encode" {
				if tokens, err := tokenizer.Encode(ctx, "text"); tokens != nil || !errors.Is(err, context.Canceled) {
					t.Fatalf("Encode = %v, %v", tokens, err)
				}
				return
			}
			if text, err := tokenizer.Decode(ctx, []int{1}); text != "" || !errors.Is(err, context.Canceled) {
				t.Fatalf("Decode = %q, %v", text, err)
			}
		})
	}
}

func TestZeroTokenizerCannotDecodeAnEmptyInput(t *testing.T) {
	if text, err := (Tokenizer{}).Decode(t.Context(), nil); text != "" || !errors.Is(err, ErrUninitialized) {
		t.Fatalf("Decode = %q, %v", text, err)
	}
}
