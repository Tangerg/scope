// Package tokenwindow preserves source text when vocabulary tokens split UTF-8.
package tokenwindow

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/tokenizer"
)

// Prefix finds a non-empty, lossless text prefix within the token window.
// A zero count means the budget cannot hold a complete source character.
func Prefix(ctx context.Context, decoder tokenizer.Decoder, source string, tokens []int, limit int) (string, int, error) {
	for count := min(limit, len(tokens)); count > 0; count-- {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		decoded, err := decoder.Decode(ctx, tokens[:count])
		if err != nil {
			return "", 0, err
		}
		if decoded != "" && utf8.ValidString(decoded) && strings.HasPrefix(source, decoded) {
			return decoded, count, nil
		}
	}
	return "", 0, nil
}
