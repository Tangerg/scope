package keyword_test

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore/filter/keyword"
)

func Example() {
	fields := metadata.Map{"city": json.RawMessage(`"Paris"`), "tags": json.RawMessage(`["Paris"]`), "empty": json.RawMessage("null")}
	terms, err := keyword.Project(fields)
	if err != nil {
		panic(err)
	}
	fmt.Println(slices.Contains(terms, keyword.String("city", "Paris")))
	fmt.Println(slices.Contains(terms, keyword.String("tags", "Paris")))
	fmt.Println(slices.Contains(terms, keyword.Presence("empty")))
	// Output:
	// true
	// false
	// false
}
