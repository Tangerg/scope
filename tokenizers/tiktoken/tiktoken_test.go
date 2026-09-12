package tiktoken_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/tokenizers/tiktoken"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	tk, err := tiktoken.New(tiktoken.CL100KBase)
	if err != nil {
		t.Fatal(err)
	}

	const want = "hello world"
	encoded, err := tk.Encode(t.Context(), want)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) == 0 {
		t.Fatal("encoded token list is empty")
	}

	got, err := tk.Decode(t.Context(), encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Decode(Encode(%q)) = %q", want, got)
	}
}

func TestCountText(t *testing.T) {
	tk, err := tiktoken.New(tiktoken.CL100KBase)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tk.CountText(t.Context(), "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if got <= 0 {
		t.Fatalf("CountText() = %d, want > 0", got)
	}
}

func TestOperationsHonorCanceledContext(t *testing.T) {
	tk, err := tiktoken.New(tiktoken.CL100KBase)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := tk.Encode(ctx, "hello"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Encode() error = %v, want context.Canceled", err)
	}
	if _, err := tk.Decode(ctx, []int{1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Decode() error = %v, want context.Canceled", err)
	}
	if _, err := tk.CountText(ctx, "hello"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CountText() error = %v, want context.Canceled", err)
	}
}

func TestNewRejectsUnknownEncoding(t *testing.T) {
	for _, name := range []string{"", "   ", "nope-such-encoding"} {
		if _, err := tiktoken.New(tiktoken.Encoding(name)); !errors.Is(err, tiktoken.ErrInvalidEncoding) {
			t.Fatalf("New(%q) error = %v, want ErrInvalidEncoding", name, err)
		}
	}
}

func TestZeroValueTokenizerReturnsError(t *testing.T) {
	var tk tiktoken.Tokenizer
	if _, err := tk.Encode(t.Context(), "hello"); !errors.Is(err, tiktoken.ErrUninitialized) {
		t.Fatalf("zero-value Tokenizer.Encode error = %v", err)
	}
}

func TestEncodingValidate(t *testing.T) {
	if err := tiktoken.CL100KBase.Validate(); err != nil {
		t.Fatalf("CL100KBase.Validate() error = %v", err)
	}
	if err := tiktoken.Encoding("unknown").Validate(); !errors.Is(err, tiktoken.ErrInvalidEncoding) {
		t.Fatalf("unknown Encoding.Validate() error = %v, want ErrInvalidEncoding", err)
	}
}
