package storetest

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// ProvenanceConformance checks that typed source identity and locations survive
// a storage round trip without becoming searchable business fields. roundTrip
// writes the supplied document to an isolated store and queries it with the given
// predicate, using either native selection or an explicit budgeted scan.
func ProvenanceConformance(t *testing.T, roundTrip func(*testing.T, *document.Document, filter.Predicate) (*vectorstore.SearchResponse, error)) {
	t.Helper()
	origin := document.NewProvenance("private://guide?lang=中文", []byte("original source bytes"))
	origin.Chunks = []document.Chunk{{Parent: "chapter", Index: 1, Total: 3}, {Parent: "section", Index: 0, Total: 2}}
	origin.Bytes = &document.ByteRange{Start: 3, End: 7}
	origin.Page = 4
	for name, facts := range map[string]metadata.Map{
		"null business metadata":     nil,
		"empty business metadata":    {},
		"independent business names": {"source": json.RawMessage(`"catalog"`), "revision": json.RawMessage(`9007199254740993`)},
	} {
		t.Run(name, func(t *testing.T) {
			want := &document.Document{ID: "00000000-0000-4000-8000-000000000001", Text: "indexed excerpt", Metadata: facts, Provenance: origin}
			// Only the searchable business source may participate, even though
			// every record has a separate, nonempty typed source.
			selection := filter.IsNull("source")
			if _, present := facts["source"]; present {
				selection = filter.EQ("source", "catalog")
			}
			res, err := roundTrip(t, want.Clone(), selection)
			if err != nil || res == nil || len(res.Results) != 1 || res.Results[0] == nil {
				t.Fatalf("business predicate did not retrieve the record: %v, %v", res, err)
			}
			got := res.Results[0].Document
			if got == nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("stored document differs: got %#v, want %#v", got, want)
			}
		})
	}
}
