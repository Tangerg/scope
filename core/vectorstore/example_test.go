package vectorstore_test

import (
	"fmt"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func Example() {
	expr := filter.EQ("category", "wildlife")
	request := &vectorstore.Query{
		Space: "habitat-v1", Values: []float64{1, 0},
		Options: vectorstore.SearchOptions{
			TopK: 5, MinScore: 0.7, Filter: expr,
		},
	}
	if err := request.ValidateFor("habitat-v1"); err != nil {
		panic(err)
	}

	fmt.Println(request.Space, request.Options.TopK, request.Options.MinScore, expr.Operator())
	// Output:
	// habitat-v1 5 0.7 ==
}
