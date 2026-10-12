package agent

import (
	"regexp"
	"strings"
	"testing"
)

// TestQualifiedNamePattern guards the two projections of the name rule: the fast
// ValidQualifiedName check and the pattern QualifiedName publishes to the schema.
func TestQualifiedNamePattern(t *testing.T) {
	compiled := regexp.MustCompile(QualifiedName("").JSONSchemaPattern())
	for _, name := range []string{
		"a", "action.prepare", "a1._-", strings.Repeat("a", maxQualifiedNameBytes),
		"", "A", "1a", "a b", "ab!", "a\n", ".a", strings.Repeat("a", maxQualifiedNameBytes+1),
	} {
		if got, want := ValidQualifiedName(name), compiled.MatchString(name); got != want {
			t.Errorf("ValidQualifiedName(%q)=%t but pattern match=%t", name, got, want)
		}
	}
}
