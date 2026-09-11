// Package tokenwindow preserves source text when vocabulary tokens split UTF-8.
package tokenwindow

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/tokenizer"
)

// Start with a small source window so selecting each chunk does not repeatedly
// encode the whole remaining document. Observed token counts drive growth;
// the probe size makes no assumption about a vocabulary's bytes per token.
const initialProbeBytes = 4 << 10

// Prefix returns a lossless source prefix whose rendered text fits the budget.
// Probe and final rendering are encoded independently because vocabulary
// boundaries can change after trimming or adding structural context. Chunk
// boundaries are selected from the probe, not a whole-document tokenization.
// An empty result means no complete source character fits.
func Prefix(ctx context.Context, codec tokenizer.Tokenizer, source string, limit int, render func(string) string) (string, error) {
	for end := min(len(source), initialProbeBytes); ; end += min(end, len(source)-end) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		for end < len(source) && !utf8.RuneStart(source[end]) {
			end++
		}
		probe := source[:end]
		tokens, err := codec.Encode(ctx, probe)
		if err != nil {
			return "", err
		}
		if len(tokens) <= limit && end < len(source) {
			continue
		}
		prefix, err := renderedPrefix(ctx, codec, probe, tokens, limit, render)
		if err != nil || prefix != "" || end == len(source) {
			return prefix, err
		}
	}
}

func renderedPrefix(ctx context.Context, codec tokenizer.Tokenizer, source string, tokens []int, limit int, render func(string) string) (string, error) {
	var prefix string
	for count := min(limit, len(tokens)); count > 0; count-- {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		decoded, err := codec.Decode(ctx, tokens[:count])
		if err != nil {
			return "", err
		}
		if decoded != "" && utf8.ValidString(decoded) && strings.HasPrefix(source, decoded) {
			prefix = decoded
			break
		}
	}
	for prefix != "" {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		rendered := render(prefix)
		measured, err := codec.Encode(ctx, rendered)
		if err != nil {
			return "", err
		}
		if rendered != "" && len(measured) <= limit {
			return prefix, nil
		}
		_, size := utf8.DecodeLastRuneInString(prefix)
		prefix = prefix[:len(prefix)-size]
	}
	return "", nil
}
