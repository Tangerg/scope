package markdown_test

import (
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/Tangerg/scope/etl/markdown"
)

func TestIndentedCodePreserved(t *testing.T) {
	source := "    secret := 1\n    print(secret)\n"
	s, err := markdown.NewSplitter(markdown.SplitterConfig{Tokenizer: runeTokenizer{}, MaxTokensPerChunk: 1000})
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
	tree := goldmark.DefaultParser().Parse(text.NewReader([]byte(chunks[0])))
	if _, ok := tree.FirstChild().(*ast.CodeBlock); !ok {
		t.Fatalf("code became %s after split: %q", tree.FirstChild().Kind(), chunks[0])
	}
}
