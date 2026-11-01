package inmemory_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/vectorstore/inmemory"
)

func Example() {
	store, err := inmemory.NewStore(context.Background(), inmemory.StoreConfig{Space: testSpace, Dimensions: 4})
	if err != nil {
		panic(err)
	}
	fmt.Println(store.Len())
	// Output: 0
}
