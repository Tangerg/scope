package mongodb

import (
	"regexp"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// LIKE matches the whole value and compares case-sensitively, so the compiled
// regex has to be anchored and carry no options. Expectations are spelled out
// rather than compared against filter.Match so this store's test does not
// depend on Core's evaluator being released first.
func TestLikeCompilesToAnchoredCaseSensitiveRegex(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		pattern string
		matches []string
		misses  []string
	}{
		{
			pattern: "Alice",
			matches: []string{"Alice"},
			misses:  []string{"alice", "ALICE", "Alicia", "Malice", ""},
		},
		{
			pattern: "Alice%",
			matches: []string{"Alice", "Alicexyz"},
			misses:  []string{"alicexyz", "MAlice"},
		},
		{
			pattern: "%Ali%",
			matches: []string{"Ali", "MAlice", "xAliy"},
			misses:  []string{"ali", "malice"},
		},
		{
			pattern: "A_ice",
			matches: []string{"A_ice", "Alice", "Abice"},
			misses:  []string{"Aice", "Abbice", "a_ice"},
		},
		{
			// A regex metacharacter in the pattern stays literal.
			pattern: "a.c",
			matches: []string{"a.c"},
			misses:  []string{"abc", "axc"},
		},
		{
			pattern: "C++%",
			matches: []string{"C++", "C++11"},
			misses:  []string{"C", "CX+"},
		},
	} {
		t.Run(sample.pattern, func(t *testing.T) {
			matcher := compileLikePattern(t, sample.pattern)
			for _, candidate := range sample.matches {
				if !matcher.MatchString(candidate) {
					t.Fatalf("pattern %q should match %q", sample.pattern, candidate)
				}
			}
			for _, candidate := range sample.misses {
				if matcher.MatchString(candidate) {
					t.Fatalf("pattern %q should not match %q", sample.pattern, candidate)
				}
			}
		})
	}
}

// compileLikePattern runs one LIKE through the visitor and returns the regex it
// produced, failing if the clause widens the match with an options flag.
func compileLikePattern(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	expression, err := filter.Parse("author like '" + pattern + "'")
	if err != nil {
		t.Fatalf("parse %q: %v", pattern, err)
	}
	visitor := newVisitor(DefaultMetadataField)
	if acceptErr := expression.Accept(visitor); acceptErr != nil {
		t.Fatalf("compile %q: %v", pattern, acceptErr)
	}
	compiled := visitor.snapshot()
	clause, ok := compiled[DefaultMetadataField+".author"].(map[string]any)
	if !ok {
		t.Fatalf("compiled %q = %#v, want a field clause", pattern, compiled)
	}
	if options, widened := clause["$options"]; widened {
		t.Fatalf("compiled %q carries $options %v, widening the match", pattern, options)
	}
	source, ok := clause["$regex"].(string)
	if !ok {
		t.Fatalf("compiled %q = %#v, want a $regex string", pattern, clause)
	}
	matcher, err := regexp.Compile(source)
	if err != nil {
		t.Fatalf("compiled regex %q for pattern %q: %v", source, pattern, err)
	}
	return matcher
}
