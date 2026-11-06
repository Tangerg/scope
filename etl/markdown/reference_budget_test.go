package markdown_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/markdown"
)

func TestReferenceExpansionIsBoundedBeforeTokenizationOrPublication(t *testing.T) {
	for _, test := range []struct{ name, definition string }{
		{name: "destination", definition: "https://example.com/" + strings.Repeat("x", 4096)},
		{name: "escaped title", definition: "/path \"" + strings.Repeat("&", 256) + "\""},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := "# Title\n\n" + strings.Repeat("[r] ", 512) + "\n\n[r]: " + test.definition + "\n"
			const maxBytes = 8192
			budget, err := etl.NewSourceBudget(maxBytes)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := markdown.NewReader(strings.NewReader(source), markdown.ReaderConfig{
				HeadingSplitLevel: 1, SourceName: "reference.md", SourceBudget: budget, MaxExpandedBytes: maxBytes,
			})
			if err != nil {
				t.Fatal(err)
			}
			if docs, readErr := reader.Read(t.Context()); !errors.Is(readErr, markdown.ErrReferenceExpansionTooLarge) || docs != nil {
				t.Fatalf("reader: documents=%v error=%v", docs, readErr)
			}
			codec := &referenceTokenizer{}
			splitter, err := markdown.NewTextSplitter(markdown.TextSplitterConfig{
				Tokenizer: codec, MaxChunks: 1, MaxExpandedBytes: maxBytes,
			})
			if err != nil {
				t.Fatal(err)
			}
			if chunks, splitErr := splitter.SplitText(t.Context(), source); !errors.Is(splitErr, markdown.ErrReferenceExpansionTooLarge) || chunks != nil || codec.calls != 0 {
				t.Fatalf("splitter: chunks=%v tokenizer calls=%d error=%v", chunks, codec.calls, splitErr)
			}
		})
	}
}

func TestReferenceBudgetUsesEscapedBytesAndPreservesSourceEvidence(t *testing.T) {
	const (
		source   = "# H\n\n[r] [r]\n\n[r]: /a \"A & B\"\n"
		body     = "# H\n\n[r](</a> \"A &amp; B\") [r](</a> \"A &amp; B\")"
		expanded = body + "\n\n[r]: /a \"A & B\"\n"
	)
	wantLinks := []linkMeaning{{destination: "/a", title: "A & B"}, {destination: "/a", title: "A & B"}}
	for _, maxBytes := range []int{len(expanded) - 1, len(expanded)} {
		reader, err := markdown.NewReader(strings.NewReader(source), markdown.ReaderConfig{
			HeadingSplitLevel: 1, SourceName: "reference.md", MaxExpandedBytes: maxBytes,
		})
		if err != nil {
			t.Fatal(err)
		}
		docs, readErr := reader.Read(t.Context())
		splitter, err := markdown.NewTextSplitter(markdown.TextSplitterConfig{Tokenizer: runeTokenizer{}, MaxExpandedBytes: maxBytes})
		if err != nil {
			t.Fatal(err)
		}
		chunks, splitErr := splitter.SplitText(t.Context(), source)
		if maxBytes < len(expanded) {
			if docs != nil || chunks != nil || !errors.Is(readErr, markdown.ErrReferenceExpansionTooLarge) || !errors.Is(splitErr, markdown.ErrReferenceExpansionTooLarge) {
				t.Fatalf("under limit: docs=%v chunks=%q read=%v split=%v", docs, chunks, readErr, splitErr)
			}
			continue
		}
		if readErr != nil || splitErr != nil || len(docs) != 1 || len(chunks) != 1 || chunks[0] != body || docs[0].Text != body {
			t.Fatalf("exact limit: docs=%v chunks=%q read=%v split=%v", docs, chunks, readErr, splitErr)
		}
		if !reflect.DeepEqual(linksIn(docs[0].Text), wantLinks) {
			t.Fatalf("link meaning changed: %v", linksIn(docs[0].Text))
		}
		origin := document.NewProvenance("reference.md", []byte(source))
		if docs[0].Provenance.Revision != origin.Revision || docs[0].Provenance.Bytes != nil {
			t.Fatalf("expanded text replaced source evidence: %+v", docs[0].Provenance)
		}
	}
}

func TestReferenceBudgetDefaultsAndConstructorAdmission(t *testing.T) {
	if reader, err := markdown.NewReader(strings.NewReader(""), markdown.ReaderConfig{MaxExpandedBytes: -1}); reader != nil || err == nil {
		t.Fatalf("negative reader limit: %v %v", reader, err)
	}
	if splitter, err := markdown.NewTextSplitter(markdown.TextSplitterConfig{Tokenizer: runeTokenizer{}, MaxExpandedBytes: -1}); splitter != nil || err == nil {
		t.Fatalf("negative splitter limit: %v %v", splitter, err)
	}
	source := strings.Repeat("x", markdown.DefaultMaxExpandedBytes+1)
	splitter, err := markdown.NewTextSplitter(markdown.TextSplitterConfig{Tokenizer: runeTokenizer{}})
	if err != nil {
		t.Fatal(err)
	}
	if chunks, splitErr := splitter.SplitText(t.Context(), source); chunks != nil || !errors.Is(splitErr, markdown.ErrReferenceExpansionTooLarge) {
		t.Fatalf("default limit: chunks=%v error=%v", chunks, splitErr)
	}
	reader, err := markdown.NewReader(strings.NewReader("[r]\n\n[r]: /a"), markdown.ReaderConfig{MaxExpandedBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if docs, readErr := reader.Read(t.Context()); readErr != nil || len(docs) != 1 || docs[0].Text != "[r]\n\n[r]: /a" {
		t.Fatalf("whole-source read expanded references: docs=%v error=%v", docs, readErr)
	}
}

func TestReferenceBudgetBoundsUnexpandedSource(t *testing.T) {
	const source = "# Heading\n\nA source without reference links.\n"
	reader, err := markdown.NewReader(strings.NewReader(source), markdown.ReaderConfig{
		HeadingSplitLevel: 1, MaxExpandedBytes: len(source) - 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if docs, readErr := reader.Read(t.Context()); docs != nil || !errors.Is(readErr, markdown.ErrReferenceExpansionTooLarge) {
		t.Fatalf("unexpanded source exceeds limit: docs=%v error=%v", docs, readErr)
	}
}

type referenceTokenizer struct{ calls int }

func (r *referenceTokenizer) Encode(context.Context, string) ([]int, error) {
	r.calls++
	return nil, errors.New("tokenization must follow reference admission")
}

func (r *referenceTokenizer) Decode(context.Context, []int) (string, error) {
	r.calls++
	return "", errors.New("decoding must follow reference admission")
}
