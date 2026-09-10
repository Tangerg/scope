package vectorstore_test

import (
	"fmt"

	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/rag"
	ragvectorstore "github.com/Tangerg/scope/rag/vectorstore"
)

func ExampleFilterValueKey() {
	predicate, err := filter.Parse(`tenant == 'acme'`)
	if err != nil {
		panic(err)
	}
	query, err := rag.NewQuery("release notes")
	if err != nil {
		panic(err)
	}
	query, err = query.WithValue(ragvectorstore.FilterValueKey(), predicate)
	if err != nil {
		panic(err)
	}
	_, found, err := query.Value(ragvectorstore.FilterValueKey())
	if err != nil {
		panic(err)
	}
	fmt.Println(query.Text(), found)
	// Output: release notes true
}
