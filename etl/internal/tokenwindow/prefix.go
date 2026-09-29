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

// Prefix returns a lossless source prefix whose rendered text fits limit tokens.
// Probe and final rendering are encoded independently because vocabulary
// boundaries can change after trimming or adding structural context, and token
// counts need not be monotonic. Token-prefix decoding only selects a candidate;
// when it finds none, every source rune boundary is checked by independent
// encoding. An empty result proves that no nonempty rendered prefix fits.
// maxWork bounds input bytes passed to render and Encode plus tokens passed to
// Decode; each operation costs at least one unit.
func Prefix(ctx context.Context, codec tokenizer.Tokenizer, source string, limit, maxWork int, render func(string) string) (string, error) {
	search := &prefixSearch{codec: codec, source: source, limit: limit, render: render, remaining: maxWork}
	for end := min(len(source), initialProbeBytes); ; end += min(end, len(source)-end) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		end = search.runeBoundary(end)
		probe := source[:end]
		tokens, err := search.encode(ctx, probe)
		if err != nil {
			return "", err
		}
		if len(tokens) <= limit && end < len(source) {
			continue
		}
		prefix, err := search.renderedPrefix(ctx, probe, tokens)
		if err != nil || prefix != "" {
			return prefix, err
		}
		prefix, err = search.scanBoundaries(ctx, end)
		if err != nil || prefix != "" {
			return prefix, err
		}
		if end == len(source) {
			return "", nil
		}
	}
}

type prefixSearch struct {
	codec     tokenizer.Tokenizer
	source    string
	limit     int
	render    func(string) string
	remaining int
	// searched is the source offset below which every rune boundary has been
	// measured, so growing the probe never repeats the exhaustive scan.
	searched int
}

func (p *prefixSearch) spend(work int) error {
	work = max(1, work)
	if work > p.remaining {
		return ErrSearchBudgetExceeded
	}
	p.remaining -= work
	return nil
}

func (p *prefixSearch) encode(ctx context.Context, text string) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.spend(len(text)); err != nil {
		return nil, err
	}
	return p.codec.Encode(ctx, text)
}

func (p *prefixSearch) renderText(source string) (string, error) {
	if err := p.spend(len(source)); err != nil {
		return "", err
	}
	return p.render(source), nil
}

func (p *prefixSearch) runeBoundary(end int) int {
	for end < len(p.source) && !utf8.RuneStart(p.source[end]) {
		end++
	}
	return end
}

// fits reports whether candidate renders to nonempty text within the limit.
func (p *prefixSearch) fits(ctx context.Context, candidate string) (bool, error) {
	rendered, err := p.renderText(candidate)
	if err != nil || rendered == "" {
		return false, err
	}
	measured, err := p.encode(ctx, rendered)
	if err != nil {
		return false, err
	}
	return len(measured) <= p.limit, nil
}

func (p *prefixSearch) renderedPrefix(ctx context.Context, probe string, tokens []int) (string, error) {
	prefix, err := p.decodedPrefix(ctx, probe, tokens)
	if err != nil {
		return "", err
	}
	for prefix != "" {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fits, err := p.fits(ctx, prefix)
		if err != nil {
			return "", err
		}
		if fits {
			return prefix, nil
		}
		_, size := utf8.DecodeLastRuneInString(prefix)
		prefix = prefix[:len(prefix)-size]
	}
	return "", nil
}

func (p *prefixSearch) decodedPrefix(ctx context.Context, probe string, tokens []int) (string, error) {
	for count := min(p.limit, len(tokens)); count > 0; count-- {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := p.spend(count); err != nil {
			return "", err
		}
		decoded, err := p.codec.Decode(ctx, tokens[:count])
		if err != nil {
			return "", err
		}
		if decoded != "" && utf8.ValidString(decoded) && strings.HasPrefix(probe, decoded) {
			return decoded, nil
		}
	}
	return "", nil
}

func (p *prefixSearch) scanBoundaries(ctx context.Context, end int) (string, error) {
	for p.searched < end {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		_, size := utf8.DecodeRuneInString(p.source[p.searched:])
		p.searched += size
		candidate := p.source[:p.searched]
		fits, err := p.fits(ctx, candidate)
		if err != nil {
			return "", err
		}
		if fits {
			return candidate, nil
		}
	}
	return "", nil
}
