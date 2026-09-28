package cassandra

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestFilterCannotSelectPhysicalColumns(t *testing.T) {
	store := &Store{metadataColumns: []MetadataColumn{{Name: "tenant", CQLType: "text"}}}
	for _, name := range []string{"id", "content", "embedding", "metadata", "unknown"} {
		predicate := filter.EQ(name, "victim")
		if _, _, err := store.buildFilter(predicate); err == nil || !strings.Contains(err.Error(), "undeclared metadata") {
			t.Fatalf("accepted %s: %v", name, err)
		}
		if err := store.DeleteWhere(t.Context(), predicate); err == nil {
			t.Fatalf("DeleteWhere accepted %s", name)
		}
		if _, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "x", Options: vectorstore.SearchOptions{Filter: predicate}}); err == nil {
			t.Fatalf("Search accepted %s", name)
		}
	}
	predicate, args, err := store.buildFilter(filter.EQ("tenant", "victim"))
	if err != nil || predicate != `"tenant" = ?` || len(args) != 1 || args[0] != "victim" {
		t.Fatalf("declared filter = %q %v %v", predicate, args, err)
	}
}
