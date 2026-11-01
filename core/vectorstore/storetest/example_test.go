package storetest_test

import (
	"fmt"

	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func Example() {
	capabilities := storetest.Capabilities{Upserter: true, Querier: true}
	fmt.Println(capabilities.Upserter, capabilities.Querier)
	// Output: true true
}
