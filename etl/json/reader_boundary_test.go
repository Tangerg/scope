package json_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/etl"
	etljson "github.com/Tangerg/scope/etl/json"
)

func read(t *testing.T, source string, config etljson.ReaderConfig) ([]string, error) {
	t.Helper()
	reader, err := etljson.NewReader(strings.NewReader(source), config)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := reader.Read(t.Context())
	if err != nil {
		return nil, err
	}
	texts := make([]string, 0, len(documents))
	for _, doc := range documents {
		texts = append(texts, doc.Text)
	}
	return texts, nil
}

// TestReaderSplitsOnlyTopLevelArrays pins the one structural decision this
// reader makes: a top-level array becomes one document per element, and every
// other JSON value stays a single document. Getting it wrong silently changes
// the retrieval unit.
func TestReaderSplitsOnlyTopLevelArrays(t *testing.T) {
	cases := map[string]struct {
		source string
		want   int
	}{
		"array":                {source: `[{"a":1},{"a":2},{"a":3}]`, want: 3},
		"empty array":          {source: `[]`, want: 0},
		"object":               {source: `{"a":1,"nested":[1,2,3]}`, want: 1},
		"string":               {source: `"just a string"`, want: 1},
		"number":               {source: `42`, want: 1},
		"boolean":              {source: `true`, want: 1},
		"array behind padding": {source: "  \n\t[1,2]", want: 2},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			documents, err := read(t, testCase.source, etljson.ReaderConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if len(documents) != testCase.want {
				t.Fatalf("produced %d documents, want %d: %#v", len(documents), testCase.want, documents)
			}
		})
	}
}

func TestReaderPreservesJSONNumbersOutsideFloat64Range(t *testing.T) {
	for _, testCase := range []struct {
		source string
		want   []string
	}{
		{source: `1e400`, want: []string{`1e400`}},
		{source: `-1e400`, want: []string{`-1e400`}},
		{source: `{"large":1e400,"small":1e-400}`, want: []string{`{"large":1e400,"small":1e-400}`}},
		{source: `[1e400,{"small":1e-400}]`, want: []string{`1e400`, `{"small":1e-400}`}},
	} {
		t.Run(testCase.source, func(t *testing.T) {
			documents, err := read(t, testCase.source, etljson.ReaderConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(documents, testCase.want) {
				t.Fatalf("document text = %q, want %q", documents, testCase.want)
			}
		})
	}
}

// TestReaderRejectsMalformedSourcesWithoutPartialOutput keeps a broken source
// from producing a half-read corpus that looks complete.
func TestReaderRejectsMalformedSourcesWithoutPartialOutput(t *testing.T) {
	cases := map[string]string{
		"truncated object": `{"a":`,
		"truncated array":  `[{"a":1},`,
		"trailing comma":   `[1,2,]`,
		"bare text":        `not json at all`,
		"empty":            ``,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			documents, err := read(t, source, etljson.ReaderConfig{})
			if err == nil {
				t.Fatalf("malformed source produced %d documents", len(documents))
			}
			if documents != nil {
				t.Fatalf("a failed read still produced %d documents", len(documents))
			}
		})
	}
}

// TestReaderHonorsTheSourceBudget is the memory contract: a whole-source reader
// must refuse an oversized input rather than truncate it into documents that
// look whole.
func TestReaderHonorsTheSourceBudget(t *testing.T) {
	large := "[" + strings.Repeat(`"padding",`, 100) + `"end"]`
	budget, err := etl.NewSourceBudget(16)
	if err != nil {
		t.Fatal(err)
	}
	documents, readErr := read(t, large, etljson.ReaderConfig{SourceBudget: budget})
	if !errors.Is(readErr, etl.ErrSourceTooLarge) {
		t.Fatalf("Read error = %v, want ErrSourceTooLarge", readErr)
	}
	if documents != nil {
		t.Fatalf("an over-budget read produced %d documents", len(documents))
	}
}

// TestReaderObservesCancellation keeps a canceled read from returning a corpus
// the caller no longer wants.
func TestReaderObservesCancellation(t *testing.T) {
	reader, err := etljson.NewReader(strings.NewReader(`[1,2,3]`), etljson.ReaderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	documents, err := reader.Read(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read error = %v, want context.Canceled", err)
	}
	if documents != nil {
		t.Fatalf("a canceled read produced %d documents", len(documents))
	}
}

func TestNewReaderRejectsANilSource(t *testing.T) {
	if _, err := etljson.NewReader(nil, etljson.ReaderConfig{}); err == nil {
		t.Fatal("NewReader accepted a nil source")
	}
}
