// Package tokenwindow preserves source text when vocabulary tokens split UTF-8.
package tokenwindow

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/tokenizer"
)

// Prefix returns a lossless source prefix whose rendered text fits the budget.
// The caller consumes source bytes and re-encodes the remaining text: vocabulary
// boundaries are not stable after trimming or adding structural context.
// An empty result means no complete source character fits.
func Prefix(ctx context.Context, codec tokenizer.Tokenizer, source string, limit int, render func(string) string) (string, error) {
	tokens, err := codec.Encode(ctx, source)
	if err != nil {
		return "", err
	}
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
