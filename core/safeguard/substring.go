package safeguard

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/text/cases"
)

// SubstringMatcherConfig names the terms to block and controls matching and
// disclosure. Matching uses Unicode case
// folding by default, including multi-rune expansions such as ß to ss. It does
// not normalize Unicode or equate visually similar characters. CaseSensitive
// compares the original bytes. HideMatch prevents a configured term from
// entering UnsafeError or OnBlock.
type SubstringMatcherConfig struct {
	Terms         []string
	CaseSensitive bool
	HideMatch     bool
}

// SubstringMatcher is immutable. It trims and deduplicates terms while preserving
// declaration order for the first match. Disclosure never changes the block decision.
type SubstringMatcher struct {
	terms        []substringTerm
	config       SubstringMatcherConfig
	overlapBytes int
}

type substringTerm struct {
	display string
	match   string
}

func NewSubstringMatcher(config SubstringMatcherConfig) (*SubstringMatcher, error) {
	cleaned := make([]substringTerm, 0, len(config.Terms))
	seen := make(map[string]struct{}, len(config.Terms))
	longest := 0
	for _, term := range config.Terms {
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
		longest = max(longest, len(match))
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("%w: at least one non-empty substring is required", ErrInvalidSubstringConfig)
	}
	config.Terms = nil
	return &SubstringMatcher{terms: cleaned, config: config, overlapBytes: longest - 1}, nil
}

func (s *SubstringMatcher) Match(ctx context.Context, text string) (Match, error) {
	return s.NewScan().Add(ctx, text)
}

func (s *SubstringMatcher) NewScan() Scan {
	return &substringScan{matcher: s}
}

func (s *SubstringMatcher) match(text string) Match {
	for _, term := range s.terms {
		if !strings.Contains(text, term.match) {
			continue
		}
		if s.config.HideMatch {
			return Match{Found: true}
		}
		return Match{Term: term.display, Found: true}
	}
	return Match{}
}

type substringScan struct {
	matcher *SubstringMatcher
	tail    string
	match   Match
}

func (s *substringScan) Add(ctx context.Context, fragment string) (Match, error) {
	if err := ctx.Err(); err != nil {
		return Match{}, err
	}
	if s.match.Found || fragment == "" {
		return s.match, nil
	}
	if !s.matcher.config.CaseSensitive {
		fragment = cases.Fold().String(fragment)
	}
	text := s.tail + fragment
	s.match = s.matcher.match(text)
	// The folded suffix can start inside a rune: matching is byte-based and it
	// is never folded again. Copying prevents retention of a large provider chunk.
	s.tail = strings.Clone(text[max(0, len(text)-s.matcher.overlapBytes):])
	return s.match, nil
}
