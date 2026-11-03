package markdown_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"

	"github.com/Tangerg/scope/etl/markdown"
)

type linkMeaning struct {
	image       bool
	destination string
	title       string
}

func linksIn(source string) []linkMeaning {
	raw := []byte(source)
	var links []linkMeaning
	_ = ast.Walk(parser.New().Parse(raw), func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch link := node.(type) {
		case *ast.Link:
			links = append(links, linkMeaning{destination: link.Destination.Value(raw), title: link.Title.Value(raw)})
		case *ast.Image:
			links = append(links, linkMeaning{image: true, destination: link.Destination.Value(raw), title: link.Title.Value(raw)})
		}
		return ast.WalkContinue, nil
	})
	return links
}

func TestReferencesRemainSelfContainedAcrossSections(t *testing.T) {
	sources := []string{
		"[manual]: https://example.com/manual\n\n# Guide\n\nRead [the manual][manual].\n",
		"# Guide\n\n[DOC] and [Doc][] and [the manual][doc].\n\n[doc]: <https://example.com/a?x=1&y=2> \"A &amp; B\"\n[DOC]: https://wrong.invalid\n\n# Cover\n\n![cover][image]\n\n[image]: https://example.com/cover.png 'cover title'\n",
		"# Nested\n\n[![cover][image]][manual]\n\n[image]: https://example.com/a.png\n[manual]: https://example.com/manual\n",
		"# Escaped\n\n[the \\] label][manual]\n\n[manual]: <https://example.com/a\\>b> \"A \\\"quote\\\" &amp; B\"\n",
		"# Multiline\n\n[the manual][long\n label]\n\n[long label]: https://example.com/manual\n  \"multiple\n  title lines\"\n",
	}
	for _, source := range sources {
		want := linksIn(source)
		if len(want) == 0 {
			t.Fatalf("invalid reference fixture: %q", source)
		}
		t.Run("splitter", func(t *testing.T) {
			splitter, err := markdown.NewTextSplitter(markdown.TextSplitterConfig{Tokenizer: runeTokenizer{}, MaxTokensPerChunk: 1000})
			if err != nil {
				t.Fatal(err)
			}
			chunks, err := splitter.SplitText(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			var got []linkMeaning
			for _, chunk := range chunks {
				got = append(got, linksIn(chunk)...)
				assertWithinLimit(t, chunk, 1000)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("links = %#v; want %#v; chunks = %q", got, want, chunks)
			}
		})
		t.Run("heading reader", func(t *testing.T) {
			reader, err := markdown.NewReader(strings.NewReader(source), markdown.ReaderConfig{HeadingSplitLevel: 1})
			if err != nil {
				t.Fatal(err)
			}
			docs, err := reader.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var got []linkMeaning
			for _, doc := range docs {
				got = append(got, linksIn(doc.Text)...)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("links = %#v; want %#v; docs = %v", got, want, docs)
			}
		})
	}
}

func TestReferenceDestinationCountsAgainstChunkBudget(t *testing.T) {
	source := "prefix words " + strings.Repeat("more ", 20) + "[manual] " + strings.Repeat("after ", 20) + "\n\n[manual]: https://example.com/manual\n"
	splitter := newSplitter(t, 60, 0)
	chunks, err := splitter.SplitText(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	var links []linkMeaning
	for _, chunk := range chunks {
		assertWithinLimit(t, chunk, 60)
		links = append(links, linksIn(chunk)...)
	}
	if !reflect.DeepEqual(links, linksIn(source)) {
		t.Fatalf("reference was severed: chunks = %q", chunks)
	}

	source = "[manual]\n\n[manual]: https://example.com/" + strings.Repeat("x", 100) + "\n"
	if chunks, err := splitter.SplitText(t.Context(), source); chunks != nil || !errors.Is(err, markdown.ErrSemanticUnitTooLarge) {
		t.Fatalf("oversized reference = %q, %v", chunks, err)
	}
}

func TestReferenceSyntaxInCodeRemainsLiteral(t *testing.T) {
	source := "```markdown\n[manual][id]\n```\n\n[id]: https://example.com/manual\n"
	chunks, err := newSplitter(t, 100, 0).SplitText(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0] != "```markdown\n[manual][id]\n```" {
		t.Fatalf("code changed: %q", chunks)
	}
}
