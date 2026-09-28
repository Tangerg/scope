package markdown_test

import (
	"errors"
	"strings"
	"testing"

	markdownsplitter "github.com/Tangerg/scope/etl/markdown"
)

func TestSplitterNamesTheIndivisibleUnit(t *testing.T) {
	cases := map[string]struct {
		source string
		unit   string
	}{
		"table row": {
			source: "| Name | Value |\n| --- | --- |\n| impossibly-long-row | value |",
			unit:   "table row",
		},
		"list item": {
			source: "- an item far longer than the configured chunk budget allows",
			unit:   "list item",
		},
		"fenced code line": {
			source: "```go\nconst indivisiblyLongIdentifierNameForThisTest = 1\n```",
			unit:   "code line",
		},
		"indented code line": {
			source: "    const indivisiblyLongIdentifierNameForThisTest = 1",
			unit:   "code line",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			splitter := newSplitter(t, 25, 0)
			_, err := splitter.SplitText(t.Context(), testCase.source)
			if !errors.Is(err, markdownsplitter.ErrSemanticUnitTooLarge) {
				t.Fatalf("SplitText error = %v, want ErrSemanticUnitTooLarge", err)
			}
			if !strings.Contains(err.Error(), testCase.unit+" requires") {
				t.Fatalf("error does not name the %s that failed: %v", testCase.unit, err)
			}
			if !strings.Contains(err.Error(), "maximum is 25") {
				t.Fatalf("error does not state the budget it exceeded: %v", err)
			}
		})
	}
}

func TestSplitterSplitsAParagraphRatherThanFailing(t *testing.T) {
	splitter := newSplitter(t, 25, 0)
	chunks, err := splitter.SplitText(t.Context(), strings.Repeat("word ", 40))
	if err != nil {
		t.Fatalf("a long paragraph failed instead of splitting: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("a long paragraph produced %d chunk(s)", len(chunks))
	}
	for _, chunk := range chunks {
		assertWithinLimit(t, chunk, 25)
	}
}
