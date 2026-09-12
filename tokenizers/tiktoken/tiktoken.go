package tiktoken

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tiktokenlib "github.com/pkoukk/tiktoken-go"

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
	O200KBase  = Encoding(tiktokenlib.MODEL_O200K_BASE)
	CL100KBase = Encoding(tiktokenlib.MODEL_CL100K_BASE)
	P50KBase   = Encoding(tiktokenlib.MODEL_P50K_BASE)
	P50KEdit   = Encoding(tiktokenlib.MODEL_P50K_EDIT)
	R50KBase   = Encoding(tiktokenlib.MODEL_R50K_BASE)
)

func (e Encoding) Validate() error {
	_, err := e.load()
	return err
}

func (e Encoding) load() (*tiktokenlib.Tiktoken, error) {
	name := string(e)
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("%w: name must not be blank", ErrInvalidEncoding)
	}
	encoding, err := tiktokenlib.GetEncoding(name)
	if err != nil {
		return nil, fmt.Errorf("%w: load %q: %w", ErrInvalidEncoding, name, err)
	}
	return encoding, nil
}

var (
	_ tokenizer.TextCounter = Tokenizer{}
	_ tokenizer.Tokenizer   = Tokenizer{}
)

// Tokenizer encodes, decodes, and counts text with one tiktoken vocabulary.
// It is safe for concurrent use.
type Tokenizer struct {
	encoding *tiktokenlib.Tiktoken
}

// New resolves the vocabulary once at construction rather than per call, so a
// misspelled encoding fails where it is configured instead of on the first
// count. The vocabulary must be named explicitly because no single encoding is
// correct across models, and guessing one silently miscounts every budget
// derived from it.
func New(encoding Encoding) (Tokenizer, error) {
	native, err := encoding.load()
	if err != nil {
		return Tokenizer{}, err
	}
	return Tokenizer{encoding: native}, nil
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
	return t.encoding.Encode(text, nil, nil), nil
}

func (t Tokenizer) Decode(ctx context.Context, tokens []int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := t.validate(); err != nil {
		return "", err
	}
	return t.encoding.Decode(tokens), nil
}

func (t Tokenizer) validate() error {
	if t.encoding == nil {
		return ErrUninitialized
	}
	return nil
}
