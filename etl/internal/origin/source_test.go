package origin_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/internal/origin"
)

func TestOriginalBytesRequireCompleteReadAndUsableIdentity(t *testing.T) {
	raw := []byte("original")
	limit, err := etl.NewSourceBudget(int64(len(raw) - 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, source string
		budget       etl.SourceBudget
		want         error
	}{
		{"truncated", "edition", limit, etl.ErrSourceTooLarge}, {"invalid name", "name\xff", etl.SourceBudget{}, document.ErrInvalidDocument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, p, readErr := origin.Read(t.Context(), bytes.NewReader(raw), tc.budget, tc.source)
			if !errors.Is(readErr, tc.want) || data != nil || p != nil {
				t.Fatalf("partially attested input: %q %+v %v", data, p, readErr)
			}
		})
	}
	data, p, err := origin.Read(t.Context(), strings.NewReader("original"), etl.SourceBudget{}, "")
	if err != nil || !bytes.Equal(data, raw) || p != nil || origin.Span(p, 0, int64(len(raw))) != nil {
		t.Fatalf("anonymous input: %q %+v %v", data, p, err)
	}
}

func TestSplitLocationsCannotEscapeCancellationOrAliasSource(t *testing.T) {
	p := document.NewProvenance("edition", []byte("original"))
	p.Bytes = &document.ByteRange{End: 8}
	exact := origin.Span(p, 1, 5)
	exact.Bytes.End = 4
	if p.Bytes.End != 8 {
		t.Fatal("exact slice mutated the source range")
	}
	docs := []*document.Document{{Text: "left"}, {Text: "right"}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, in := range [][]*document.Document{nil, docs} {
		out, err := origin.Parts(ctx, p, in)
		if !errors.Is(err, context.Canceled) || out != nil {
			t.Fatalf("canceled read exposed %v, %v", out, err)
		}
	}
	out, err := origin.Parts(t.Context(), p, docs)
	if err != nil || len(out) != 2 {
		t.Fatalf("parts=%v, %v", out, err)
	}
	for i, d := range out {
		if d.Provenance.Bytes != nil || d.Provenance.Revision != p.Revision || len(d.Provenance.Chunks) != 1 || d.Provenance.Chunks[0] != (document.Chunk{Index: i, Total: 2}) {
			t.Fatalf("nonverbatim part: %+v", d.Provenance)
		}
	}
}
