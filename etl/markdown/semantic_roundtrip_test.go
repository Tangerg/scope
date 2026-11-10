package markdown_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"

	"github.com/Tangerg/scope/etl/markdown"
)

func TestSplitFencedCodeRetainsParsedContent(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		for _, fence := range []string{"````", "~~~~"} {
			source := strings.Join([]string{"  " + fence + "go", "  first()", "  ```", "  third()", "  " + fence, " \t", "# Next", "text"}, newline)
			splitter := newSplitter(t, len(fence+"go\nfirst()\n"+fence), 4)
			chunks, err := splitter.SplitText(t.Context(), source)
			if err != nil || len(chunks) != 4 {
				t.Fatalf("chunks=%q error=%v", chunks, err)
			}
			var code []string
			for _, chunk := range chunks[:3] {
				raw := []byte(chunk)
				tree := parser.New().Parse(raw)
				block, ok := tree.FirstChild().(*ast.CodeBlock)
				if !ok || block.NextSibling() != nil || block.Info.Value(raw) != "go" {
					t.Fatalf("chunk lost its single code block or info: %q", chunk)
				}
				code = append(code, strings.TrimSuffix(block.Value.Str(raw), "\n"))
			}
			if !slices.Equal(code, []string{"first()", "```", "third()"}) {
				t.Fatalf("parsed code changed: %q", code)
			}
		}
	}
}

func TestIndentedCodePreserved(t *testing.T) {
	source := "    secret := 1\n    print(secret)\n"
	s, err := markdown.NewTextSplitter(markdown.TextSplitterConfig{Tokenizer: runeTokenizer{}, MaxTokensPerChunk: 1000})
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := s.SplitText(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks: %q", chunks)
	}
	if chunks[0] != "    secret := 1\n    print(secret)" {
		t.Fatalf("code indentation changed: %q", chunks[0])
	}
	tree := parser.New().Parse([]byte(chunks[0]))
	if _, ok := tree.FirstChild().(*ast.CodeBlock); !ok {
		t.Fatalf("code became %s after split: %q", tree.FirstChild().Kind(), chunks[0])
	}
}
