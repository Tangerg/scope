package tiktoken_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/Tangerg/scope/tokenizers/tiktoken"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	tk, err := tiktoken.New(t.Context(), tiktoken.CL100KBase)
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

func TestEmbeddedVocabulariesHaveExactTokenIDs(t *testing.T) {
	for _, test := range []struct {
		encoding     tiktoken.Encoding
		want         []int
		reserved     []int
		reservedText string
	}{
		{tiktoken.CL100KBase, []int{15339, 1917}, []int{100257, 100258, 100259, 100260, 100276}, "<|endoftext|><|fim_prefix|><|fim_middle|><|fim_suffix|><|endofprompt|>"},
		{tiktoken.O200KBase, []int{24912, 2375}, []int{199999, 200018}, "<|endoftext|><|endofprompt|>"},
		{tiktoken.R50KBase, []int{31373, 995}, []int{50256}, "<|endoftext|>"},
		{tiktoken.P50KBase, []int{31373, 995}, []int{50256}, "<|endoftext|>"},
		{tiktoken.P50KEdit, []int{31373, 995}, []int{50256, 50281, 50282, 50283}, "<|endoftext|><|fim_prefix|><|fim_middle|><|fim_suffix|>"},
	} {
		t.Run(string(test.encoding), func(t *testing.T) {
			codec, err := tiktoken.New(t.Context(), test.encoding)
			if err != nil {
				t.Fatal(err)
			}
			got, err := codec.Encode(t.Context(), "hello world")
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Encode = %v, %v; want %v", got, err, test.want)
			}
			decoded, err := codec.Decode(t.Context(), test.reserved)
			if err != nil || decoded != test.reservedText {
				t.Fatalf("Decode reserved = %q, %v; want %q", decoded, err, test.reservedText)
			}
			for _, text := range []string{"你好，世界", "café\n\tdata", "<|endoftext|>"} {
				tokens, err := codec.Encode(t.Context(), text)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := codec.Decode(t.Context(), tokens)
				if err != nil || decoded != text {
					t.Fatalf("round trip %q = %q, %v", text, decoded, err)
				}
			}
		})
	}
}

func TestNewHonorsCancellationAndCodecIsConcurrent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := tiktoken.New(ctx, tiktoken.O200KBase); !errors.Is(err, context.Canceled) {
		t.Fatalf("New canceled = %v", err)
	}
	codec, err := tiktoken.New(t.Context(), tiktoken.CL100KBase)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 5 {
				tokens, err := codec.Encode(t.Context(), "hello world")
				if err != nil {
					t.Error(err)
					return
				}
				decoded, err := codec.Decode(t.Context(), tokens)
				if err != nil || decoded != "hello world" {
					t.Errorf("concurrent round trip = %q, %v", decoded, err)
					return
				}
			}
		})
	}
	workers.Wait()
}

func TestCountText(t *testing.T) {
	tk, err := tiktoken.New(t.Context(), tiktoken.CL100KBase)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tk.CountText(t.Context(), "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("CountText() = %d, want 2", got)
	}
}

func TestOperationsHonorCanceledContext(t *testing.T) {
	tk, err := tiktoken.New(t.Context(), tiktoken.CL100KBase)
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
		if _, err := tiktoken.New(t.Context(), tiktoken.Encoding(name)); !errors.Is(err, tiktoken.ErrInvalidEncoding) {
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

func TestDecodeRejectsUnknownVocabularyIDs(t *testing.T) {
	tokenizer, err := tiktoken.New(t.Context(), tiktoken.CL100KBase)
	if err != nil {
		t.Fatal(err)
	}
	for _, tokens := range [][]int{{-1}, {999999999}, {100256}, {15339, -1}} {
		got, decodeErr := tokenizer.Decode(t.Context(), tokens)
		if decodeErr == nil || got != "" {
			t.Fatalf("Decode(%v) = %q, %v", tokens, got, decodeErr)
		}
	}
	got, err := tokenizer.Decode(t.Context(), []int{100257})
	if err != nil || got != "<|endoftext|>" {
		t.Fatalf("special token = %q, %v", got, err)
	}
}
