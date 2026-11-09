package elastic

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestHitOwnsIdentitySourceRoutingAndConcurrencyAdmission(t *testing.T) {
	valid := Hit{Index: "documents", ID: "one", SeqNo: new(int64(0)), PrimaryTerm: new(int64(1)), Source: json.RawMessage(`{}`)}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	version, err := valid.Version()
	if err != nil || version.SeqNo != 0 || version.PrimaryTerm != 1 {
		t.Fatalf("version=%+v error=%v", version, err)
	}
	for _, change := range []func(*Hit){
		func(h *Hit) { h.Index = "" },
		func(h *Hit) { h.ID = "" },
		func(h *Hit) { h.ID = strings.Repeat("x", 513) },
		func(h *Hit) { h.Routing = "tenant" },
		func(h *Hit) { h.Fields = map[string][]json.RawMessage{"_routing": {json.RawMessage(`"tenant"`)}} },
		func(h *Hit) { h.SeqNo = nil },
		func(h *Hit) { h.PrimaryTerm = nil },
		func(h *Hit) { h.SeqNo = new(int64(-1)) },
		func(h *Hit) { h.PrimaryTerm = new(int64(0)) },
		func(h *Hit) { h.Source = nil },
	} {
		hit := valid
		change(&hit)
		if err := hit.Validate(); err == nil {
			t.Fatalf("invalid hit accepted: %+v", hit)
		}
	}
	if err := DocumentID("").Validate(); !errors.Is(err, vectorstore.ErrMissingDocumentID) {
		t.Fatal(err)
	}
	if err := DocumentID(strings.Repeat("x", 512)).Validate(); err != nil {
		t.Fatal(err)
	}
}
