package safeguard

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/text/cases"
)

// SubstringConfig controls matching and disclosure. Matching uses Unicode case
// folding by default, including multi-rune expansions such as ß to ss. It does
// not normalize Unicode or equate visually similar characters. CaseSensitive
// compares the original bytes. HideMatch prevents a configured term from
// entering UnsafeError or OnBlock.
type SubstringConfig struct {
	CaseSensitive bool
	HideMatch     bool
}

// SubstringMatcher is immutable. It trims and deduplicates terms while preserving
// declaration order for the first match. Disclosure never changes the block decision.
type SubstringMatcher struct {
	terms  []substringTerm
	config SubstringConfig
}

type substringTerm struct {
	display string
	match   string
}

func NewSubstringMatcher(terms []string, config SubstringConfig) (*SubstringMatcher, error) {
	cleaned := make([]substringTerm, 0, len(terms))
	seen := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		display := strings.TrimSpace(term)
		if display == "" {
			continue
		}
		match := display
		if !config.CaseSensitive {
			match = cases.Fold().String(match)
		}
		if _, duplicate := seen[match]; duplicate {
			continue
		}
		seen[match] = struct{}{}
		cleaned = append(cleaned, substringTerm{display: display, match: match})
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("%w: at least one non-empty substring is required", ErrInvalidSubstringConfig)
	}
	return &SubstringMatcher{terms: cleaned, config: config}, nil
}

func (s *SubstringMatcher) Match(ctx context.Context, text string) (Match, error) {
	if err := ctx.Err(); err != nil {
		return Match{}, err
	}
	if text == "" {
		return Match{}, nil
	}
	haystack := text
	if !s.config.CaseSensitive {
		haystack = cases.Fold().String(haystack)
	}
	for _, term := range s.terms {
		if !strings.Contains(haystack, term.match) {
			continue
		}
		if s.config.HideMatch {
			return Match{Found: true}, nil
		}
		return Match{Term: term.display, Found: true}, nil
	}
	return Match{}, nil
}
