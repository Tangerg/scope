package safeguard_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/safeguard"
)

func Example() {
	matcher, err := safeguard.NewSubstringMatcher(safeguard.SubstringMatcherConfig{Terms: []string{"secret"}})
	if err != nil {
		panic(err)
	}
	match, err := matcher.Match(context.Background(), "do not reveal the SECRET")
	if err != nil {
		panic(err)
	}
	fmt.Println(match.Found, match.Term)
	// Output: true secret
}
