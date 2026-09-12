// Package tokenwindow preserves source text when vocabulary tokens split UTF-8.
package tokenwindow

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/tokenizer"
)

// Start with a small source window so selecting each chunk does not repeatedly
// encode the whole remaining document. Observed token counts drive growth;
// the probe size makes no assumption about a vocabulary's bytes per token.
const initialProbeBytes = 4 << 10

// ErrSearchBudgetExceeded means the search stopped without proving absence.
var ErrSearchBudgetExceeded = errors.New("tokenwindow: search budget exceeded")

type searchBudget struct{ remaining int }

func (s *searchBudget) spend(work int) error {
	work = max(1, work)
	if work > s.remaining {
		return ErrSearchBudgetExceeded
	}
	s.remaining -= work
	return nil
}

func (s *searchBudget) encode(ctx context.Context, codec tokenizer.Tokenizer, text string) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.spend(len(text)); err != nil {
		return nil, err
	}
	return codec.Encode(ctx, text)
}

func (s *searchBudget) render(source string, render func(string) string) (string, error) {
	if err := s.spend(len(source)); err != nil {
		return "", err
	}
	return render(source), nil
}

// Prefix returns a lossless source prefix whose rendered text fits the budget.
// Probe and final rendering are encoded independently because vocabulary
// boundaries can change after trimming or adding structural context. Chunk
// boundaries are selected from the probe, not a whole-document tokenization.
// Token-prefix decoding is only a candidate selector: if it finds no admissible
// prefix, every source rune boundary is checked by independent encoding. Token
// counts need not be monotonic. An empty result proves that no nonempty rendered
// source prefix fits. maxWork bounds input bytes passed to render and Encode,
// plus tokens passed to Decode; each operation costs at least one unit.
func Prefix(ctx context.Context, codec tokenizer.Tokenizer, source string, limit, maxWork int, render func(string) string) (string, error) {
	budget := &searchBudget{remaining: maxWork}
	searched := 0
	for end := min(len(source), initialProbeBytes); ; end += min(end, len(source)-end) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		for end < len(source) && !utf8.RuneStart(source[end]) {
			end++
		}
		probe := source[:end]
		tokens, err := budget.encode(ctx, codec, probe)
		if err != nil {
			return "", err
		}
		if len(tokens) <= limit && end < len(source) {
			continue
		}
		prefix, err := budget.renderedPrefix(ctx, codec, probe, tokens, limit, render)
		if err != nil || prefix != "" {
			return prefix, err
		}
		for searched < end {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			_, size := utf8.DecodeRuneInString(source[searched:])
			searched += size
			candidate := source[:searched]
			rendered, err := budget.render(candidate, render)
			if err != nil {
				return "", err
			}
			if rendered == "" {
				continue
			}
			measured, err := budget.encode(ctx, codec, rendered)
			if err != nil {
				return "", err
			}
			if len(measured) <= limit {
				return candidate, nil
			}
		}
		if end == len(source) {
			return "", nil
		}
	}
}

func (s *searchBudget) renderedPrefix(ctx context.Context, codec tokenizer.Tokenizer, source string, tokens []int, limit int, render func(string) string) (string, error) {
	var prefix string
	for count := min(limit, len(tokens)); count > 0; count-- {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := s.spend(count); err != nil {
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
		rendered, err := s.render(prefix, render)
		if err != nil {
			return "", err
		}
		measured, err := s.encode(ctx, codec, rendered)
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
