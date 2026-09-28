package tiktoken

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tiktokenlib "github.com/tiktoken-go/tokenizer"

	"github.com/Tangerg/scope/core/tokenizer"
)

var (
	ErrInvalidEncoding = errors.New("tiktoken: invalid encoding")
	ErrUninitialized   = errors.New("tiktoken: tokenizer is not initialized")
)

// Encoding identifies a tiktoken vocabulary. Callers choose explicitly because
// token counts are vocabulary-specific; there is no model-independent default.
type Encoding string

// Supported encodings.
const (
	O200KBase  = Encoding(tiktokenlib.O200kBase)
	CL100KBase = Encoding(tiktokenlib.Cl100kBase)
	P50KBase   = Encoding(tiktokenlib.P50kBase)
	P50KEdit   = Encoding(tiktokenlib.P50kEdit)
	R50KBase   = Encoding(tiktokenlib.R50kBase)
)

// Validate checks only vocabulary identity; it performs no I/O or initialization.
func (e Encoding) Validate() error {
	switch e {
	case O200KBase, CL100KBase, P50KBase, P50KEdit, R50KBase:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidEncoding, e)
	}
}

// The offline codec does not expose reserved-token decoding. These are the
// vocabulary's published IDs, not text-matching rules; ordinary input still
// passes through the codec without interpreting reserved marker strings.
func (e Encoding) reservedTokens() map[int]string {
	switch e {
	case O200KBase:
		return map[int]string{199999: "<|endoftext|>", 200018: "<|endofprompt|>"}
	case CL100KBase:
		return map[int]string{100257: "<|endoftext|>", 100258: "<|fim_prefix|>", 100259: "<|fim_middle|>", 100260: "<|fim_suffix|>", 100276: "<|endofprompt|>"}
	case P50KEdit:
		return map[int]string{50256: "<|endoftext|>", 50281: "<|fim_prefix|>", 50282: "<|fim_middle|>", 50283: "<|fim_suffix|>"}
	default:
		return map[int]string{50256: "<|endoftext|>"}
	}
}

var (
	_ tokenizer.TextCounter = Tokenizer{}
	_ tokenizer.Tokenizer   = Tokenizer{}
)

// Tokenizer encodes, decodes, and counts text with one tiktoken vocabulary.
// It is safe for concurrent use.
type Tokenizer struct {
	encoding tiktokenlib.Codec
	reserved map[int]string
}

// New initializes one explicitly selected, embedded vocabulary. Construction
// never downloads dictionaries or reads or writes a cache.
func New(ctx context.Context, encoding Encoding) (Tokenizer, error) {
	if err := ctx.Err(); err != nil {
		return Tokenizer{}, err
	}
	if err := encoding.Validate(); err != nil {
		return Tokenizer{}, err
	}
	native, err := tiktokenlib.Get(tiktokenlib.Encoding(encoding))
	if err != nil {
		return Tokenizer{}, fmt.Errorf("tiktoken: initialize %q: %w", encoding, err)
	}
	// The codec lazily initializes its reverse vocabulary without synchronization.
	// Finish that mutation before sharing this otherwise immutable instance.
	if _, err := native.Decode(nil); err != nil {
		return Tokenizer{}, fmt.Errorf("tiktoken: initialize reverse vocabulary: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Tokenizer{}, err
	}
	return Tokenizer{encoding: native, reserved: encoding.reservedTokens()}, nil
}

func (t Tokenizer) CountText(ctx context.Context, text string) (int, error) {
	tokens, err := t.Encode(ctx, text)
	if err != nil {
		return 0, err
	}
	return len(tokens), nil
}

func (t Tokenizer) Encode(ctx context.Context, text string) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	native, _, err := t.encoding.Encode(text)
	if err != nil {
		return nil, fmt.Errorf("tiktoken: encode: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tokens := make([]int, len(native))
	for index, token := range native {
		tokens[index] = int(token)
	}
	return tokens, nil
}

func (t Tokenizer) Decode(ctx context.Context, tokens []int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := t.validate(); err != nil {
		return "", err
	}
	var text strings.Builder
	for index, token := range tokens {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		decoded, reserved := t.reserved[token]
		if !reserved {
			if token < 0 {
				return "", fmt.Errorf("tiktoken: decode token[%d]: ID %d is outside the vocabulary", index, token)
			}
			var err error
			decoded, err = t.encoding.Decode([]uint{uint(token)})
			if err != nil {
				return "", fmt.Errorf("tiktoken: decode token[%d]: %w", index, err)
			}
		}
		text.WriteString(decoded)
	}
	return text.String(), nil
}

func (t Tokenizer) validate() error {
	if t.encoding == nil {
		return ErrUninitialized
	}
	return nil
}
