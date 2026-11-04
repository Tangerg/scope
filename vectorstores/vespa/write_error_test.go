package vespa

import (
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestWriteReceiptPartitionsHaveIndependentStorage(t *testing.T) {
	store, fixture := newFixtureStore(t, StoreConfig{})
	fixture.putResponse = func(id string) any {
		if id == "uncertain" {
			id = "wrong"
		}
		return map[string]string{"id": store.nativeID(id)}
	}
	docs := []*document.Document{{ID: "ok", Text: "t"}, {ID: "uncertain", Text: "t"}, {ID: "later", Text: "t"}}
	err := store.Upsert(t.Context(), fixtureBatch(docs))
	receipt, ok := errors.AsType[*vectorstore.WriteError](err)
	if !ok || !slices.Equal(receipt.Acknowledged, []string{"ok"}) || !slices.Equal(receipt.Unconfirmed, []string{"uncertain"}) || !slices.Equal(receipt.Unattempted, []string{"later"}) {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	merged := append(receipt.Acknowledged, "another-operation")
	if !slices.Equal(merged, []string{"ok", "another-operation"}) || !slices.Equal(receipt.Unconfirmed, []string{"uncertain"}) || !slices.Equal(receipt.Unattempted, []string{"later"}) {
		t.Fatalf("appending acknowledgment changed other receipt facts: unknown=%v, untouched=%v", receipt.Unconfirmed, receipt.Unattempted)
	}
}
